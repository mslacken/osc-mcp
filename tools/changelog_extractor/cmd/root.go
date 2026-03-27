package cmd

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/cavaliergopher/cpio"
	"github.com/hbollon/go-edlib"
	"github.com/openSUSE/osc-mcp/internal/pkg/osc"
	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/ulikunitz/xz"
)

var (
	cfgFile string
	nLines  int
	debug   bool
)

// type Config struct {
// 	Files []string `yaml:"files"`
// }

type Output struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Changelog        string            `json:"changelog"`
	ArchiveChangelog string            `json:"archive_changelog"`
	AddedFiles       []string          `json:"added_files"`
	RemovedFiles     []string          `json:"removed_files"`
	ExtractedFiles   map[string]string `json:"extracted_files"`
	Source           string            `json:"source"`
	GitHubRelease    string            `json:"github_release_notes"`
	SpecDiff         string            `json:"spec_diff"`
}

type RevisionList struct {
	XMLName   xml.Name   `xml:"revisionlist"`
	Revisions []Revision `xml:"revision"`
}

type Revision struct {
	Rev    string `xml:"rev,attr"`
	SrcMD5 string `xml:"srcmd5"`
}

type Directory struct {
	XMLName xml.Name `xml:"directory"`
	Entries []Entry  `xml:"entry"`
}

type Entry struct {
	Name string `xml:"name,attr"`
	Md5  string `xml:"md5,attr"`
}

type PackageTarget struct {
	Project  string
	Package  string
	Revision string
}

var rootCmd = &cobra.Command{
	Use:   "changelog_extractor [project] [package] [revision]",
	Short: "Extract changelog, added/removed files, and specified files from OBS",
	Args:  cobra.ExactArgs(3),
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		logLevel := slog.LevelInfo
		if debug {
			logLevel = slog.LevelDebug
		}
		handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: logLevel,
		})
		slog.SetDefault(slog.New(handler))
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		target := PackageTarget{
			Project:  args[0],
			Package:  args[1],
			Revision: args[2],
		}

		credsVal, err := osc.GetCredentials()
		if err != nil {
			return fmt.Errorf("failed to get osc credentials: %w", err)
		}
		creds := &credsVal

		prevRevision, err := getPreviousRevision(creds, target)
		if err != nil {
			slog.Warn("could not determine previous revision, assuming none", "error", err)
		}

		currentFiles, err := getDirectory(creds, target)
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}

		var prevFiles *Directory
		var prevTarget PackageTarget
		prevTargetValid := false
		if prevRevision != "" {
			prevTarget = target
			prevTarget.Revision = prevRevision
			prevTargetValid = true
			prevFiles, err = getDirectory(creds, prevTarget)
			if err != nil {
				slog.Warn("failed to get previous directory, assuming none", "error", err)
			}
		}

		added, removed := compareDirectories(prevFiles, currentFiles)

		var filesToDownload []string
		changesFile := ""
		for _, e := range currentFiles.Entries {
			if strings.HasSuffix(e.Name, ".changes") {
				changesFile = e.Name
				filesToDownload = append(filesToDownload, e.Name)
				break
			}
		}
		if changesFile == "" {
			return fmt.Errorf("Couldn't find a valid changes file")
		}

		// Identify the best matching .spec file
		specFiles := []string{}
		for _, e := range currentFiles.Entries {
			if strings.HasSuffix(e.Name, ".spec") {
				specFiles = append(specFiles, e.Name)
			}
		}
		bestSpec := ""
		if len(specFiles) > 1 {
			bestSpec, _ = edlib.FuzzySearch(target.Package, specFiles, edlib.Levenshtein)
		} else if len(specFiles) == 1 {
			bestSpec = specFiles[0]
		}
		if bestSpec != "" {
			filesToDownload = append(filesToDownload, bestSpec)
		}

		extracted := make(map[string]string)
		var changelog string
		var matchedSource string
		var specSource SourceFile
		var archiveChangelog string
		var specURL string
		var githubOwner, githubRepo string
		var githubReleaseNotes string
		var specDiff string

		if len(filesToDownload) > 0 {
			downloadedBytes, err := downloadFiles(creds, target, filesToDownload)
			if err != nil {
				slog.Warn("failed to download initial files", "error", err)
			}

			if b, ok := downloadedBytes[changesFile]; ok {
				changelog = extractLatestChangelog(string(b))
			}

			if bestSpec != "" {
				if b, ok := downloadedBytes[bestSpec]; ok {
					if prevTargetValid && prevFiles != nil {
						hasPrevSpec := false
						for _, e := range prevFiles.Entries {
							if e.Name == bestSpec {
								hasPrevSpec = true
								break
							}
						}
						if hasPrevSpec {
							prevDownloaded, err := downloadFiles(creds, prevTarget, []string{bestSpec})
							if err == nil {
								if pb, pok := prevDownloaded[bestSpec]; pok {
									diff := difflib.UnifiedDiff{
										A:        difflib.SplitLines(string(pb)),
										B:        difflib.SplitLines(string(b)),
										FromFile: bestSpec + " (prev)",
										ToFile:   bestSpec + " (curr)",
										Context:  3,
									}
									specDiff, _ = difflib.GetUnifiedDiffString(diff)
									if specDiff == "" {
										slog.Debug("SpecDiff is empty, but files were downloaded", "len_pb", len(pb), "len_b", len(b))
									} else {
										slog.Debug("SpecDiff generated successfully", "len_diff", len(specDiff))
									}
								} else {
									slog.Warn("bestSpec not found in prevDownloaded map")
								}
							} else {
								fmt.Printf("prevTargetValid=%v, bestSpec=%s\n", prevTargetValid, bestSpec)
								slog.Warn("failed to download previous spec file for diff", "error", err)
							}
						}
					}

					specValues := extractSourceFromSpec(string(b), []string{"Name", "Version", "Source", "URL"})
					specSource.Version = specValues["Version"]
					specSource.Name = specValues["Source"]
					specURL = specValues["URL"]
					rawSource := specValues["Source"]
					name := specValues["Name"]

					specURL = expandMacros(specURL, name, specSource.Version)
					rawSource = expandMacros(rawSource, name, specSource.Version)

					// Try to extract github repo from URL, then fallback to Source
					githubOwner, githubRepo = parseGitHubOwnerRepo(specURL)
					if githubOwner == "" || githubRepo == "" {
						githubOwner, githubRepo = parseGitHubOwnerRepo(rawSource)
					}

					if specSource.Name != "" {
						specSource.Name = expandMacros(specSource.Name, name, specSource.Version)
						if idx := strings.LastIndex(specSource.Name, "/"); idx != -1 {
							specSource.Name = specSource.Name[idx+1:]
						}

						if matchedSourceFile, versionChanged := findSourceMatch(specSource, currentFiles.Entries); versionChanged {
							specSource.Version = matchedSourceFile.Version
							matchedSource = matchedSourceFile.Name
						} else {
							matchedSource = matchedSourceFile.Name
						}
					}
				}
			}

			// If we found a source that wasn't in our initial download list, download it now
			if matchedSource != "" {
				if _, ok := downloadedBytes[matchedSource]; !ok {
					sourceBytes, err := downloadFiles(creds, target, []string{matchedSource})
					if err != nil {
						slog.Warn("failed to download source archive", "error", err)
					} else {
						if b, ok := sourceBytes[matchedSource]; ok {
							downloadedBytes[matchedSource] = b
						}
					}
				}
			}

			if matchedSource != "" {
				if b, ok := downloadedBytes[matchedSource]; ok {
					archiveChangelog, err = extractChangelogFromArchive(b, matchedSource, nLines)
					if err != nil {
						slog.Warn("failed to extract changelog from archive", "error", err)
					}
				}
			}
		}

		if githubOwner != "" && githubRepo != "" {
			githubReleaseNotes = fetchGitHubReleaseNotes(githubOwner, githubRepo, specSource.Version)
			if githubReleaseNotes != "" {
				githubReleaseNotes = extractFirstNLines(strings.NewReader(githubReleaseNotes), nLines)
			}
		}

		out := Output{
			Name:             target.Package,
			Version:          specSource.Version,
			Changelog:        changelog,
			ArchiveChangelog: archiveChangelog,
			AddedFiles:       added,
			RemovedFiles:     removed,
			ExtractedFiles:   extracted,
			Source:           matchedSource,
			GitHubRelease:    githubReleaseNotes,
			SpecDiff:         specDiff,
		}

		if out.AddedFiles == nil {
			out.AddedFiles = []string{}
		}
		if out.RemovedFiles == nil {
			out.RemovedFiles = []string{}
		}
		if out.ExtractedFiles == nil {
			out.ExtractedFiles = make(map[string]string)
		}

		outBytes, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal output JSON: %w", err)
		}

		fmt.Println(string(outBytes))

		return nil
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().StringVarP(&cfgFile, "config", "c", "", "YAML config file containing list of files to extract")
	rootCmd.Flags().IntVarP(&nLines, "n-lines", "n", 20, "Number of lines to extract from internal changelog")
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "Enable debug logging")
	viper.BindPFlag("config", rootCmd.Flags().Lookup("config"))
	viper.BindPFlag("n_lines", rootCmd.Flags().Lookup("n-lines"))
}

func isChangelogFile(filename string) bool {
	base := strings.ToLower(filename)
	// Remove path if any
	if idx := strings.LastIndex(base, "/"); idx != -1 {
		base = base[idx+1:]
	}

	prefixes := []string{"changelog", "changlog", "changes", "news"}
	for _, p := range prefixes {
		if strings.HasPrefix(base, p) {
			return true
		}
	}
	return false
}

func extractFirstNLines(r io.Reader, n int) string {
	if n <= 0 {
		return ""
	}
	scanner := bufio.NewScanner(r)
	var lines []string
	for i := 0; i < n && scanner.Scan(); i++ {
		lines = append(lines, scanner.Text())
	}
	return strings.Join(lines, "\n")
}

func extractChangelogFromArchive(archiveBytes []byte, filename string, n int) (string, error) {
	slog.Debug("called extractChangelogFromArchive", "filename", filename, "size", len(archiveBytes))
	if n <= 0 {
		return "", nil
	}

	reader := bytes.NewReader(archiveBytes)

	// Determine if we need to decompress
	var r io.Reader = reader
	var err error

	// Handle common compressions
	if strings.HasSuffix(filename, ".gz") || strings.HasSuffix(filename, ".tgz") {
		slog.Debug("using gzip reader")
		r, err = gzip.NewReader(r)
		if err != nil {
			return "", err
		}
	} else if strings.HasSuffix(filename, ".bz2") {
		slog.Debug("using bzip2 reader")
		r = bzip2.NewReader(r)
	} else if strings.HasSuffix(filename, ".xz") {
		slog.Debug("using xz reader")
		r, err = xz.NewReader(r)
		if err != nil {
			return "", err
		}
	}

	// Now handle archive format
	if strings.Contains(filename, ".cpio") || strings.Contains(filename, ".obscpio") {
		slog.Debug("using cpio archive reader")
		cr := cpio.NewReader(r)
		for {
			header, err := cr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				slog.Debug("cpio reader error", "error", err)
				return "", err
			}
			slog.Debug("cpio file", "name", header.Name)
			if isChangelogFile(header.Name) && header.Mode.IsRegular() {
				slog.Debug("found changelog match in cpio", "name", header.Name)
				return extractFirstNLines(cr, n), nil
			}
		}
	} else if strings.HasSuffix(filename, ".zip") {
		slog.Debug("using zip archive reader")
		// zip needs a ReaderAt, so we use the original bytes
		zr, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
		if err != nil {
			return "", err
		}
		for _, f := range zr.File {
			slog.Debug("zip file", "name", f.Name)
			if isChangelogFile(f.Name) && !f.FileInfo().IsDir() {
				slog.Debug("found changelog match in zip", "name", f.Name)
				rc, err := f.Open()
				if err != nil {
					return "", err
				}
				content := extractFirstNLines(rc, n)
				rc.Close()
				return content, nil
			}
		}
	} else if isChangelogFile(filename) {
		slog.Debug("file is a standalone changelog file")
		// If the downloaded source is a standalone changelog file
		return extractFirstNLines(r, n), nil
	} else {
		slog.Debug("using fallback tar archive reader")
		// Fallback: assume tar for .tar, .tgz, .tbz2, .txz, or generic .gz/.bz2/.xz tarballs
		tr := tar.NewReader(r)
		for {
			header, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				// Not a valid tar file or unexpected error, just break
				slog.Debug("tar reader error", "error", err)
				break
			}
			if isChangelogFile(header.Name) && header.Typeflag == tar.TypeReg {
				slog.Debug("found changelog match in tar", "name", header.Name)
				return extractFirstNLines(tr, n), nil
			}
		}
	}

	slog.Debug("no changelog file found in archive")
	return "", nil
}

func getPreviousRevision(creds *osc.OSCCredentials, target PackageTarget) (string, error) {
	url := fmt.Sprintf("%s/source/%s/%s/_history", creds.GetAPiAddr(), target.Project, target.Package)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(creds.Name, creds.Passwd)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get history: %s", resp.Status)
	}

	var list RevisionList
	if err := xml.NewDecoder(resp.Body).Decode(&list); err != nil {
		return "", err
	}

	prev := ""
	slog.Debug("revisions", "list", list.Revisions)
	for _, r := range list.Revisions {
		if r.Rev == target.Revision || r.SrcMD5 == target.Revision {
			return prev, nil
		}
		prev = r.Rev
	}
	return "", fmt.Errorf("revision %s not found in history", target.Revision)
}

func getDirectory(creds *osc.OSCCredentials, target PackageTarget) (*Directory, error) {
	url := fmt.Sprintf("%s/source/%s/%s?rev=%s", creds.GetAPiAddr(), target.Project, target.Package, target.Revision)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(creds.Name, creds.Passwd)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get directory: %s", resp.Status)
	}

	var dir Directory
	if err := xml.NewDecoder(resp.Body).Decode(&dir); err != nil {
		return nil, err
	}
	return &dir, nil
}

func compareDirectories(prev, curr *Directory) (added []string, removed []string) {
	prevMap := make(map[string]bool)
	if prev != nil {
		for _, e := range prev.Entries {
			prevMap[e.Name] = true
		}
	}

	currMap := make(map[string]bool)
	if curr != nil {
		for _, e := range curr.Entries {
			currMap[e.Name] = true
			if !prevMap[e.Name] {
				added = append(added, e.Name)
			}
		}
	}

	if prev != nil {
		for _, e := range prev.Entries {
			if !currMap[e.Name] {
				removed = append(removed, e.Name)
			}
		}
	}

	return added, removed
}

func downloadFiles(creds *osc.OSCCredentials, target PackageTarget, filenames []string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for _, filename := range filenames {
		url := fmt.Sprintf("%s/source/%s/%s/%s?rev=%s", creds.GetAPiAddr(), target.Project, target.Package, filename, target.Revision)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(creds.Name, creds.Passwd)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to download file %s: %s", filename, resp.Status)
		}

		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read body for file %s: %w", filename, err)
		}
		result[filename] = b
	}
	return result, nil
}

func extractLatestChangelog(content string) string {
	marker := "-------------------------------------------------------------------"
	parts := strings.Split(content, marker)

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}

		// The first line of a non-empty part is the email/date line.
		lines := strings.SplitN(trimmed, "\n", 2)
		if len(lines) > 1 {
			return strings.TrimSpace(lines[1])
		}
	}
	return ""
}

type SourceFile struct {
	Name    string
	Version string
}

func extractSourceFromSpec(specContent string, keys []string) map[string]string {
	values := make(map[string]string)
	lines := strings.Split(specContent, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		lowerKey := strings.ToLower(key)

		for _, k := range keys {
			lowerK := strings.ToLower(k)
			if lowerKey == lowerK || lowerKey == lowerK+"0" {
				if _, exists := values[k]; !exists {
					values[k] = val
				}
			}
		}
	}
	return values
}

func expandMacros(source, name, version string) string {
	if source == "" {
		return ""
	}
	if name != "" {
		source = strings.ReplaceAll(source, "%{name}", name)
		source = strings.ReplaceAll(source, "%name", name)
	}
	if version != "" {
		source = strings.ReplaceAll(source, "%{version}", version)
		source = strings.ReplaceAll(source, "%version", version)
	}
	return source
}

func findSourceMatch(specSource SourceFile, entries []Entry) (SourceFile, bool) {
	specSourceName := specSource.Name
	if specSourceName == "" {
		return SourceFile{}, false
	}

	var bestMatch string
	// Exact match first
	for _, e := range entries {
		if e.Name == specSourceName {
			bestMatch = e.Name
			break
		}
	}

	if bestMatch == "" {
		// Try matching against .obscpio files (where the base name matches)
		for _, e := range entries {
			if strings.HasSuffix(e.Name, ".obscpio") && strings.TrimSuffix(e.Name, ".obscpio") == specSourceName {
				bestMatch = e.Name
				break
			}
		}
	}

	if bestMatch == "" {
		// Prepare list of entry names for fuzzy search
		entryNames := make([]string, 0, len(entries))
		for _, e := range entries {
			// Skip .spec and .changes as they are unlikely to be the "source" archive we want
			if strings.HasSuffix(e.Name, ".spec") || strings.HasSuffix(e.Name, ".changes") {
				continue
			}
			entryNames = append(entryNames, e.Name)
		}

		if len(entryNames) > 0 {
			// Use fuzzy search to find the best match, which helps when unexpanded macros are present
			m, err := edlib.FuzzySearch(specSourceName, entryNames, edlib.Levenshtein)
			if err == nil && m != "" {
				bestMatch = m
			}
		}
	}

	res := SourceFile{
		Name:    bestMatch,
		Version: specSource.Version,
	}
	// Try to extract version if it's a macro
	if strings.HasPrefix(specSource.Version, "%{") && strings.HasSuffix(specSource.Version, "}") {
		macro := specSource.Version
		tmpl := specSource.Name
		if strings.Contains(tmpl, macro) {
			parts := strings.SplitN(tmpl, macro, 2)
			prefix := parts[0]
			suffix := parts[1]

			if strings.HasPrefix(bestMatch, prefix) && strings.HasSuffix(bestMatch, suffix) {
				extractedVersion := bestMatch[len(prefix) : len(bestMatch)-len(suffix)]
				if extractedVersion != "" {
					res.Version = extractedVersion
					return res, true
				}
			}
		}
	}

	return res, false
}
