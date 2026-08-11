package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"servercli/internal/releasecache"
)

type gitHubClient struct {
	baseURL string
	token   string
	client  *http.Client
	mu      sync.Mutex
	cache   map[string]map[string]githubAsset
}

type githubRelease struct {
	Assets []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func newGitHubClient(baseURL, token string, timeout time.Duration) *gitHubClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.github.com"
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &gitHubClient{
		baseURL: strings.TrimRight(baseURL, "/"), token: strings.TrimSpace(token),
		client: &http.Client{Timeout: timeout}, cache: make(map[string]map[string]githubAsset),
	}
}

func (client *gitHubClient) ListArtifacts(ctx context.Context, owner, repo, tag string) ([]releasecache.ReleaseArtifactMeta, error) {
	release, err := client.fetchRelease(ctx, owner, repo, tag)
	if err != nil {
		return nil, err
	}
	checksums := make(map[string]string)
	for _, asset := range release.Assets {
		if asset.Name == "sha256sums.txt" {
			data, err := client.downloadURL(ctx, asset.BrowserDownloadURL)
			if err != nil {
				return nil, fmt.Errorf("download GitHub sha256sums.txt: %w", err)
			}
			mergeSHA256Sums(checksums, data)
		}
	}
	for _, asset := range release.Assets {
		if strings.HasPrefix(asset.Name, "release-manifest") && strings.HasSuffix(asset.Name, ".json") {
			data, err := client.downloadURL(ctx, asset.BrowserDownloadURL)
			if err != nil {
				return nil, fmt.Errorf("download GitHub release manifest %q: %w", asset.Name, err)
			}
			mergeReleaseManifestChecksums(checksums, data)
		}
	}

	assetMap := make(map[string]githubAsset, len(release.Assets))
	metas := make([]releasecache.ReleaseArtifactMeta, 0, len(release.Assets))
	for _, asset := range release.Assets {
		assetMap[asset.Name] = asset
		if asset.Name == "sha256sums.txt" || strings.HasPrefix(asset.Name, "release-manifest") && strings.HasSuffix(asset.Name, ".json") {
			continue
		}
		digest := strings.TrimPrefix(strings.TrimSpace(asset.Digest), "sha256:")
		if digest == "" {
			digest = checksums[asset.Name]
		}
		metas = append(metas, releasecache.ReleaseArtifactMeta{Name: asset.Name, Size: asset.Size, SHA256: digest})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	client.mu.Lock()
	client.cache[releaseCacheKey(owner, repo, tag)] = assetMap
	client.mu.Unlock()
	return metas, nil
}

func (client *gitHubClient) DownloadArtifact(ctx context.Context, owner, repo, tag, name string) ([]byte, error) {
	key := releaseCacheKey(owner, repo, tag)
	client.mu.Lock()
	asset, ok := client.cache[key][name]
	client.mu.Unlock()
	if !ok {
		release, err := client.fetchRelease(ctx, owner, repo, tag)
		if err != nil {
			return nil, err
		}
		for _, candidate := range release.Assets {
			if candidate.Name == name {
				asset = candidate
				ok = true
				break
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("GitHub release artifact %q not found", name)
	}
	return client.downloadURL(ctx, asset.BrowserDownloadURL)
}

func (client *gitHubClient) fetchRelease(ctx context.Context, owner, repo, tag string) (*githubRelease, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", client.baseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(tag))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	client.addGitHubHeaders(req)
	resp, err := client.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GitHub API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode GitHub release: %w", err)
	}
	return &release, nil
}

func (client *gitHubClient) downloadURL(ctx context.Context, downloadURL string) ([]byte, error) {
	if downloadURL == "" {
		return nil, errors.New("GitHub asset has no download URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	client.addGitHubHeaders(req)
	resp, err := client.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GitHub download returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(resp.Body)
}

func (client *gitHubClient) addGitHubHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "servercli-release-sync/1")
	if client.token != "" {
		req.Header.Set("Authorization", "Bearer "+client.token)
	}
}

func mergeSHA256Sums(checksums map[string]string, data []byte) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && len(fields[0]) == 64 {
			checksums[strings.TrimPrefix(fields[len(fields)-1], "*")] = strings.ToLower(fields[0])
		}
	}
}

func mergeReleaseManifestChecksums(checksums map[string]string, data []byte) {
	var manifest struct {
		Artifacts []struct {
			Path   string `json:"path"`
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
		} `json:"artifacts"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return
	}
	for _, artifact := range manifest.Artifacts {
		name := artifact.Name
		if name == "" {
			name = manifestPathToAssetName(artifact.Path)
		}
		if name != "" && artifact.SHA256 != "" {
			checksums[name] = strings.ToLower(artifact.SHA256)
		}
	}
}

func manifestPathToAssetName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasSuffix(value, "/") {
		return strings.TrimSuffix(value, "/") + ".tar.gz"
	}
	return strings.ReplaceAll(value, "/", "-")
}

func releaseCacheKey(owner, repo, tag string) string { return owner + "\x00" + repo + "\x00" + tag }
