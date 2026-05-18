package uploader

import (
	"fmt"
	"os"

	"github.com/teacat/chaturbate-dvr/server"
)

type imageHost struct {
	name   string
	upload func(string) (string, error)
}

// MultiImageUploader uploads thumbnails/sprites with durable fallbacks:
// Pixhost (NSFW API) → Catbox (permanent) → Freeimage → GitHub (if configured).
type MultiImageUploader struct {
	hosts []imageHost
}

// NewMultiImageUploader creates the default thumbnail upload chain.
func NewMultiImageUploader() *MultiImageUploader {
	pixhost := NewThumbnailUploader("")
	catbox := NewCatboxUploader()
	freeimage := NewFreeimageUploader()

	hosts := []imageHost{
		{name: "Pixhost", upload: pixhost.Upload},
		{name: "Catbox", upload: catbox.Upload},
		{name: "Freeimage", upload: freeimage.Upload},
	}

	// Add GitHub as last-resort fallback if configured
	githubToken := os.Getenv("GITHUB_TOKEN")
	githubRepo := os.Getenv("GITHUB_REPO")
	githubBranch := os.Getenv("GITHUB_BRANCH")
	githubPreviewPath := os.Getenv("GITHUB_PREVIEW_PATH")

	if server.Config != nil {
		if server.Config.GitHubToken != "" {
			githubToken = server.Config.GitHubToken
		}
		if server.Config.GitHubRepo != "" {
			githubRepo = server.Config.GitHubRepo
		}
		if server.Config.GitHubBranch != "" {
			githubBranch = server.Config.GitHubBranch
		}
		if server.Config.GitHubPreviewPath != "" {
			githubPreviewPath = server.Config.GitHubPreviewPath
		}
	}

	if githubToken != "" && githubRepo != "" {
		github := NewGitHubUploader(githubToken, githubRepo, githubBranch, githubPreviewPath)
		hosts = append(hosts, imageHost{name: "GitHub", upload: github.Upload})
	}

	return &MultiImageUploader{hosts: hosts}
}



// Upload tries each host in order until one succeeds.
func (m *MultiImageUploader) Upload(filePath string) (url, host string, err error) {
	var lastErr error
	for _, h := range m.hosts {
		url, err = h.upload(filePath)
		if err == nil {
			return url, h.name, nil
		}
		lastErr = fmt.Errorf("%s: %w", h.name, err)
	}
	return "", "", fmt.Errorf("all image hosts failed: %w", lastErr)
}
