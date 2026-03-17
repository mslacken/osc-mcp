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
