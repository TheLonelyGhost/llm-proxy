// Package github provides a minimal GitHub Releases API client used by the
// plugin installer to resolve version constraints against published releases.
//
// Only the subset of the API needed to list release tags and asset URLs is
// implemented. No authentication is required for public repositories; for
// private repositories the caller should set GITHUB_TOKEN in the environment
// and pass the resulting header via an [http.Client].
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrNoMatchingRelease is returned when the API returns releases but none
// satisfy the requested version constraint.
var ErrNoMatchingRelease = errors.New("no matching release found")

// ErrReleaseListFailed is returned when the GitHub API call fails.
var ErrReleaseListFailed = errors.New("failed to list releases")

// release is the subset of the GitHub Releases API response we care about.
type release struct {
	TagName string `json:"tag_name"`
	Draft   bool   `json:"draft"`
	// Prerelease is the GitHub "pre-release" flag; we honour it when
	// filtering candidates so that e.g. "1.0.0-rc.1" tagged as a
	// GitHub pre-release is excluded unless explicitly requested.
	Prerelease bool `json:"prerelease"`
}

// ListReleaseTags fetches all non-draft release tag names for a GitHub
// repository and returns them as a slice, newest-first as returned by the API.
//
// repoPath must be in the form "owner/repo" (no scheme or host).
// The client parameter allows callers to inject a custom [http.Client]
// (e.g. one that adds an Authorization header); pass nil to use the default.
//
// Only tags that begin with "v" followed by a digit are returned, matching
// the GitHub convention for semantic version releases.
func ListReleaseTags(ctx context.Context, client *http.Client, repoPath string) ([]string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	// GitHub paginates at 30 by default; we request up to 100 per page and
	// fetch all pages.
	var tags []string
	page := 1
	for {
		url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d", repoPath, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: build request: %w", ErrReleaseListFailed, err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrReleaseListFailed, err)
		}
		if resp == nil {
			return nil, fmt.Errorf("%w: nil response", ErrReleaseListFailed)
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("%w: read body: %w", ErrReleaseListFailed, readErr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%w: HTTP %d: %s", ErrReleaseListFailed, resp.StatusCode, body)
		}

		var batch []release
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("%w: decode response: %w", ErrReleaseListFailed, err)
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			if r.Draft || r.Prerelease {
				continue
			}
			// Only accept "v<digit>..." tags.
			if len(r.TagName) >= 2 && r.TagName[0] == 'v' && r.TagName[1] >= '0' && r.TagName[1] <= '9' {
				tags = append(tags, r.TagName)
			}
		}
		page++
	}
	return tags, nil
}
