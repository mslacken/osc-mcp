package cmd

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/hbollon/go-edlib"
	"github.com/openSUSE/osc-mcp/internal/pkg/osc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile    string
	outputFile string
)

// type Config struct {
// 	Files []string `yaml:"files"`
// }

type Output struct {
	Name           string            `json:"name"`
	Version        string            `json:"version"`
	Changelog      string            `json:"changelog"`
	AddedFiles     []string          `json:"added_files"`
	RemovedFiles   []string          `json:"removed_files"`
	ExtractedFiles map[string]string `json:"extracted_files,omitempty"`
	Source         string            `json:"source"`
}

type RevisionList struct {
	XMLName   xml.Name   `xml:"revisionlist"`
	Revisions []Revision `xml:"revision"`
}

type Revision struct {
	Rev string `xml:"rev,attr"`
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
		if prevRevision != "" {
			prevTarget := target
			prevTarget.Revision = prevRevision
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

		if len(filesToDownload) > 0 {
			downloadedBytes, err := downloadFiles(creds, target, filesToDownload)
			if err != nil {
				slog.Warn("failed to download some files", "error", err)
			}

			if b, ok := downloadedBytes[changesFile]; ok {
				changelog = extractLatestChangelog(string(b))
			}

			if bestSpec != "" {
				if b, ok := downloadedBytes[bestSpec]; ok {
					// extracted[bestSpec] = string(b)
					specSource = extractSourceFromSpec(string(b))
					if specSource.Name != "" {
						for _, e := range currentFiles.Entries {
							if e.Name == specSource.Name {
								matchedSource = e.Name
								break
							}
						}
					}
				}
			}

		}

		out := Output{
			Name:           target.Package,
			Version:        specSource.Version,
			Changelog:      changelog,
			AddedFiles:     added,
			RemovedFiles:   removed,
			ExtractedFiles: extracted,
			Source:         matchedSource,
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

		if outputFile != "" {
			if err := os.WriteFile(outputFile, outBytes, 0644); err != nil {
				return fmt.Errorf("failed to write output file: %w", err)
			}
			fmt.Printf("Output written to %s\n", outputFile)
		} else {
			fmt.Println(string(outBytes))
		}

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
	rootCmd.Flags().StringVarP(&outputFile, "output", "o", "output.json", "Output JSON file")
	viper.BindPFlag("config", rootCmd.Flags().Lookup("config"))
	viper.BindPFlag("output", rootCmd.Flags().Lookup("output"))
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
	for _, r := range list.Revisions {
		if r.Rev == target.Revision {
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

func extractSourceFromSpec(specContent string) SourceFile {
	var name, version, source string
	lines := strings.Split(specContent, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		lowerLine := strings.ToLower(line)
		if strings.HasPrefix(lowerLine, "name:") && name == "" {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				name = strings.TrimSpace(parts[1])
			}
		} else if strings.HasPrefix(lowerLine, "version:") && version == "" {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				version = strings.TrimSpace(parts[1])
			}
		} else if (strings.HasPrefix(lowerLine, "source:") || strings.HasPrefix(lowerLine, "source0:")) && source == "" {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				source = strings.TrimSpace(parts[1])
			}
		}
	}

	if source != "" {
		if idx := strings.LastIndex(source, "/"); idx != -1 {
			source = source[idx+1:]
		}
		if name != "" {
			source = strings.ReplaceAll(source, "%{name}", name)
			source = strings.ReplaceAll(source, "%name", name)
		}
		if version != "" {
			source = strings.ReplaceAll(source, "%{version}", version)
			source = strings.ReplaceAll(source, "%version", version)
		}
	}
	return SourceFile{
		Name:    source,
		Version: version,
	}
}
