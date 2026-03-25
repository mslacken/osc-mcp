package cmd

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"

	"github.com/cavaliergopher/cpio"
)

func TestExtractLatestChangelog(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "single entry",
			content: `-------------------------------------------------------------------
Wed Mar 11 12:00:00 UTC 2026 - user@example.com

- Entry 1
- Entry 2
`,
			want: "- Entry 1\n- Entry 2",
		},
		{
			name: "multiple entries",
			content: `-------------------------------------------------------------------
Wed Mar 11 12:00:00 UTC 2026 - user@example.com

- Entry 1
- Entry 2

-------------------------------------------------------------------
Tue Mar 10 12:00:00 UTC 2026 - user@example.com

- Old entry
`,
			want: "- Entry 1\n- Entry 2",
		},
		{
			name:    "empty",
			content: "",
			want:    "",
		},
		{
			name:    "no content after header",
			content: "-------------------------------------------------------------------\nWed Mar 11 12:00:00 UTC 2026 - user@example.com\n",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractLatestChangelog(tt.content); got != tt.want {
				t.Errorf("extractLatestChangelog() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractSourceFromSpec(t *testing.T) {
	tests := []struct {
		name        string
		specContent string
		want        string
	}{
		{
			name: "simple source",
			specContent: `Name: mypkg
Version: 1.0
Source: mypkg-1.0.tar.gz`,
			want: "mypkg-1.0.tar.gz",
		},
		{
			name: "source0 with macros",
			specContent: `Name: awesome-app
Version: 2.1.3
Source0: https://github.com/user/awesome-app/archive/v%{version}.tar.gz#/%{name}-%{version}.tar.gz`,
			want: "awesome-app-2.1.3.tar.gz",
		},
		{
			name: "source with name macro",
			specContent: `Name: my-lib
Version: 1.0
Source: %name.tar.bz2`,
			want: "my-lib.tar.bz2",
		},
		{
			name: "url source",
			specContent: `Name: pkg
Version: 1
Source: http://example.com/downloads/source-file.zip`,
			want: "source-file.zip",
		},
		{
			name: "source0 match",
			specContent: `Name: mypkg
Version: 1.0
Source0: mypkg-1.0.tar.gz`,
			want: "mypkg-1.0.tar.gz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := extractSourceFromSpec(tt.specContent, []string{"Name", "Version", "Source"})
			got := expandMacros(values["Source"], values["Name"], values["Version"])
			if idx := strings.LastIndex(got, "/"); idx != -1 {
				got = got[idx+1:]
			}
			if got != tt.want {
				t.Errorf("extractSourceFromSpec() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsChangelogFile(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     bool
	}{
		{"exact changelog", "Changelog", true},
		{"exact CHANGELOG", "CHANGELOG", true},
		{"with extension", "CHANGELOG.md", true},
		{"with path", "src/CHANGELOG.txt", true},
		{"changes prefix", "changes", true},
		{"news prefix", "NEWS.rst", true},
		{"misspelled changlog", "changlog.txt", true},
		{"not a changelog", "main.go", false},
		{"not a changelog but contains word", "fake_changelog.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isChangelogFile(tt.filename); got != tt.want {
				t.Errorf("isChangelogFile(%q) = %v, want %v", tt.filename, got, tt.want)
			}
		})
	}
}

func TestExtractChangelogFromArchive(t *testing.T) {
	// Create a simple tar archive in memory
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := "Line 1\nLine 2\nLine 3\nLine 4\nLine 5"
	hdr := &tar.Header{
		Name: "CHANGELOG",
		Mode: 0600,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	// Create a simple cpio archive in memory
	var cpioBuf bytes.Buffer
	cw := cpio.NewWriter(&cpioBuf)
	cpioContent := "CPIO Line 1\nCPIO Line 2"
	cpioHdr := &cpio.Header{
		Name: "CHANGELOG",
		Mode: 0644,
		Size: int64(len(cpioContent)),
	}
	if err := cw.WriteHeader(cpioHdr); err != nil {
		t.Fatal(err)
	}
	if _, err := cw.Write([]byte(cpioContent)); err != nil {
		t.Fatal(err)
	}
	if err := cw.Close(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		archive  []byte
		filename string
		n        int
		want     string
	}{
		{
			name:     "extract 2 lines from tar",
			archive:  buf.Bytes(),
			filename: "src.tar",
			n:        2,
			want:     "Line 1\nLine 2",
		},
		{
			name:     "extract 1 line from cpio",
			archive:  cpioBuf.Bytes(),
			filename: "src.obscpio",
			n:        1,
			want:     "CPIO Line 1",
		},
		{
			name:     "extract 0 lines",
			archive:  buf.Bytes(),
			filename: "src.tar",
			n:        0,
			want:     "",
		},
		{
			name:     "extract more lines than exist",
			archive:  buf.Bytes(),
			filename: "src.tar",
			n:        10,
			want:     content,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractChangelogFromArchive(tt.archive, tt.filename, tt.n)
			if err != nil {
				t.Errorf("extractChangelogFromArchive() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("extractChangelogFromArchive() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindSourceMatch(t *testing.T) {
	entries := []Entry{
		{Name: "mypkg-1.0.tar.gz"},
		{Name: "mypkg.spec"},
		{Name: "mypkg.changes"},
		{Name: "other-1.0.tar.xz"},
		{Name: "obscpio-match.obscpio"},
	}

	tests := []struct {
		name        string
		specSource  SourceFile
		want        string
		wantVersion string
		wantChanged bool
	}{
		{
			name: "exact match",
			specSource: SourceFile{
				Name: "mypkg-1.0.tar.gz",
			},
			want:        "mypkg-1.0.tar.gz",
			wantVersion: "",
			wantChanged: false,
		},
		{
			name: "unexpanded version macro",
			specSource: SourceFile{
				Name:    "mypkg-%{version}.tar.gz",
				Version: "%{version}",
			},
			want:        "mypkg-1.0.tar.gz",
			wantVersion: "1.0",
			wantChanged: true,
		},
		{
			name: "obscpio match",
			specSource: SourceFile{
				Name: "obscpio-match",
			},
			want:        "obscpio-match.obscpio",
			wantVersion: "",
			wantChanged: false,
		},
		{
			name: "fuzzy match other",
			specSource: SourceFile{
				Name:    "other-%{vers}.tar.xz",
				Version: "%{vers}",
			},
			want:        "other-1.0.tar.xz",
			wantVersion: "1.0",
			wantChanged: true,
		},
		{
			name: "typo match",
			specSource: SourceFile{
				Name: "mypkg-1.0.tar.z",
			},
			want:        "mypkg-1.0.tar.gz",
			wantVersion: "",
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := findSourceMatch(tt.specSource, entries)
			if got.Name != tt.want {
				t.Errorf("findSourceMatch(%q) name = %q, want %q", tt.specSource.Name, got.Name, tt.want)
			}
			if got.Version != tt.wantVersion {
				t.Errorf("findSourceMatch(%q) version = %q, want %q", tt.specSource.Name, got.Version, tt.wantVersion)
			}
			if changed != tt.wantChanged {
				t.Errorf("findSourceMatch(%q) changed = %v, want %v", tt.specSource.Name, changed, tt.wantChanged)
			}
		})
	}
}
