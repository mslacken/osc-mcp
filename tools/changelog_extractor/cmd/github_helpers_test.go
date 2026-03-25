package cmd

import (
	"testing"
)

func TestParseGitHubOwnerRepo(t *testing.T) {
	tests := []struct {
		url       string
		wantOwner string
		wantRepo  string
	}{
		{"https://github.com/openSUSE/osc-mcp", "openSUSE", "osc-mcp"},
		{"https://github.com/openSUSE/osc-mcp.git", "openSUSE", "osc-mcp"},
		{"https://github.com/openSUSE/osc-mcp/archive/v1.0.tar.gz", "openSUSE", "osc-mcp"},
		{"git@github.com:openSUSE/osc-mcp.git", "openSUSE", "osc-mcp"}, // won't match our simple split, but not typical in specs
		{"http://example.com/downloads/source-file.zip", "", ""},
		{"https://github.com/user/awesome-app/archive/v2.1.3.tar.gz#/awesome-app-2.1.3.tar.gz", "user", "awesome-app"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			gotOwner, gotRepo := parseGitHubOwnerRepo(tt.url)
			if gotOwner != tt.wantOwner || gotRepo != tt.wantRepo {
				t.Errorf("parseGitHubOwnerRepo(%q) = %q, %q, want %q, %q", tt.url, gotOwner, gotRepo, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}
