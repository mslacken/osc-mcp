package cmd

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/go-github/v60/github"
)

func parseGitHubOwnerRepo(url string) (string, string) {
	var suffix string
	if idx := strings.Index(url, "github.com/"); idx != -1 {
		suffix = url[idx+len("github.com/"):]
	} else if idx := strings.Index(url, "github.com:"); idx != -1 {
		suffix = url[idx+len("github.com:"):]
	} else {
		return "", ""
	}

	pathParts := strings.Split(suffix, "/")
	if len(pathParts) >= 2 {
		// handle paths like openSUSE/osc-mcp/archive/...
		repo := pathParts[1]
		// remove url parameters or fragments if any
		if idx := strings.IndexAny(repo, "?#"); idx != -1 {
			repo = repo[:idx]
		}
		return pathParts[0], strings.TrimSuffix(repo, ".git")
	}
	return "", ""
}

func fetchGitHubReleaseNotes(owner, repo, version string) string {
	if owner == "" || repo == "" || version == "" {
		return ""
	}

	client := github.NewClient(nil)
	ctx := context.Background()

	tagsToTry := []string{version, "v" + version, repo + "-" + version, "V" + version}
	for _, tag := range tagsToTry {
		release, resp, err := client.Repositories.GetReleaseByTag(ctx, owner, repo, tag)
		if err == nil && release != nil && release.Body != nil {
			return *release.Body
		}
		if resp != nil && (resp.StatusCode == 403 || resp.StatusCode == 429) {
			slog.Warn("github API rate limit hit or forbidden", "owner", owner, "repo", repo)
			break
		}
	}
	return ""
}
