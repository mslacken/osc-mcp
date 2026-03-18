package cmd

import (
	"testing"
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractSourceFromSpec(tt.specContent).Name; got != tt.want {
				t.Errorf("extractSourceFromSpec() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindSourceMatch(t *testing.T) {
	tests := []struct {
		name           string
		specSourceName string
		entries        []Entry
		want           string
	}{
		{
			name:           "exact match",
			specSourceName: "foo-1.0.tar.gz",
			entries: []Entry{
				{Name: "foo-1.0.tar.gz"},
				{Name: "foo.spec"},
			},
			want: "foo-1.0.tar.gz",
		},
		{
			name:           "obscpio match",
			specSourceName: "foo-1.0.tar.gz",
			entries: []Entry{
				{Name: "foo-1.0.tar.gz.obscpio"},
				{Name: "foo.spec"},
			},
			want: "foo-1.0.tar.gz.obscpio",
		},
		{
			name:           "no match",
			specSourceName: "foo-1.0.tar.gz",
			entries: []Entry{
				{Name: "bar-1.0.tar.gz"},
				{Name: "foo.spec"},
			},
			want: "",
		},
		{
			name:           "empty spec source",
			specSourceName: "",
			entries: []Entry{
				{Name: "foo-1.0.tar.gz"},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findSourceMatch(tt.specSourceName, tt.entries); got != tt.want {
				t.Errorf("findSourceMatch() = %q, want %q", got, tt.want)
			}
		})
	}
}
