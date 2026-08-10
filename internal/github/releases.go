// Package github provides a minimal GitHub Releases API client used by the
// plugin installer to resolve version constraints against published releases.
//
// Only the subset of the API needed to list release tags and asset URLs is
// implemented. No authentication is required for public repositories; for
// private repositories the caller should set GITHUB_TOKEN in the environment
// and pass the resulting header via an [http.Client].
package github

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrNoMatchingRelease is returned when the API returns releases but none
// satisfy the requested version constraint.
var ErrNoMatchingRelease = errors.New("no matching release found")

// ErrReleaseListFailed is returned when the GitHub API call fails.
var ErrReleaseListFailed = errors.New("failed to list releases")

// ErrChecksumFetchFailed is returned when the SHA256SUMS file cannot be
// fetched or parsed.
var ErrChecksumFetchFailed = errors.New("failed to fetch checksum file")

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

// FetchChecksumFile downloads the SHA256SUMS file at url and returns a map
// from filename to lowercase hex SHA-256 digest.
//
// Each line of the file is expected to be in the standard shasum(1) format:
//
//	<hex>  <filename>
//
// Lines that do not match this format (too few fields, blank lines) are
// silently skipped. The client parameter follows the same nil-fallback
// convention as ListReleaseTags.
func FetchChecksumFile(ctx context.Context, client *http.Client, url string) (map[string]string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", ErrChecksumFetchFailed, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrChecksumFetchFailed, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("%w: nil response", ErrChecksumFetchFailed)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrChecksumFetchFailed, resp.StatusCode, body)
	}

	out := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Standard shasum format: "<hex>  <filename>" (two spaces) or "<hex> <filename>"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		out[fields[1]] = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: read body: %w", ErrChecksumFetchFailed, err)
	}
	return out, nil
}
