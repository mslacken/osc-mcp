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

	"github.com/openSUSE/osc-mcp/internal/pkg/osc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var (
	cfgFile    string
	outputFile string
)

type Config struct {
	Files []string `yaml:"files"`
}

type Output struct {
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

var rootCmd = &cobra.Command{
	Use:   "changelog_extractor [project] [package] [revision]",
	Short: "Extract changelog, added/removed files, and specified files from OBS",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		project := args[0]
		pkg := args[1]
		revision := args[2]

		var config Config
		if cfgFile != "" {
			b, err := os.ReadFile(cfgFile)
			if err != nil {
				return fmt.Errorf("failed to read config file: %w", err)
			}
			if err := yaml.Unmarshal(b, &config); err != nil {
				return fmt.Errorf("failed to parse config file: %w", err)
			}
		}

		credsVal, err := osc.GetCredentials()
		if err != nil {
			return fmt.Errorf("failed to get osc credentials: %w", err)
		}
		creds := &credsVal

		prevRevision, err := getPreviousRevision(creds, project, pkg, revision)
		if err != nil {
			slog.Warn("could not determine previous revision, assuming none", "error", err)
		}

		currentFiles, err := getDirectory(creds, project, pkg, revision)
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}

		var prevFiles *Directory
		if prevRevision != "" {
			prevFiles, err = getDirectory(creds, project, pkg, prevRevision)
			if err != nil {
				slog.Warn("failed to get previous directory, assuming none", "error", err)
			}
		}

		added, removed := compareDirectories(prevFiles, currentFiles)

		var changelog string
		for _, e := range currentFiles.Entries {
			if strings.HasSuffix(e.Name, ".changes") {
				b, err := downloadFile(creds, project, pkg, e.Name, revision)
				if err != nil {
					slog.Warn("failed to download changes file", "file", e.Name, "error", err)
				} else {
					changelog = extractLatestChangelog(string(b))
				}
				break
			}
		}

		extracted := make(map[string]string)
		for _, filename := range config.Files {
			exists := false
			for _, e := range currentFiles.Entries {
				if e.Name == filename {
					exists = true
					break
				}
			}
			if exists {
				b, err := downloadFile(creds, project, pkg, filename, revision)
				if err != nil {
					slog.Warn("failed to download file", "file", filename, "error", err)
				} else {
					extracted[filename] = string(b)
				}
			}
		}

		out := Output{
			Changelog:      changelog,
			AddedFiles:     added,
			RemovedFiles:   removed,
			ExtractedFiles: extracted,
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

func getPreviousRevision(creds *osc.OSCCredentials, project, pkg, currentRev string) (string, error) {
	url := fmt.Sprintf("%s/source/%s/%s/_history", creds.GetAPiAddr(), project, pkg)
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
		if r.Rev == currentRev {
			return prev, nil
		}
		prev = r.Rev
	}
	return "", fmt.Errorf("revision %s not found in history", currentRev)
}

func getDirectory(creds *osc.OSCCredentials, project, pkg, revision string) (*Directory, error) {
	url := fmt.Sprintf("%s/source/%s/%s?rev=%s", creds.GetAPiAddr(), project, pkg, revision)
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

func downloadFile(creds *osc.OSCCredentials, project, pkg, filename, revision string) ([]byte, error) {
	url := fmt.Sprintf("%s/source/%s/%s/%s?rev=%s", creds.GetAPiAddr(), project, pkg, filename, revision)
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
		return nil, fmt.Errorf("failed to download file: %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
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
