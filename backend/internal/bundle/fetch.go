package bundle

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fetchFrom resolves baseURL + rel against either an https/http endpoint or a
// file:// path (used by tests and local imports) and returns the bytes. The
// read is bounded by maxBytes; a larger response is an error. HTTP requests
// honor timeout and ctx.
func fetchFrom(ctx context.Context, baseURL, rel string, timeout time.Duration, maxBytes int64) ([]byte, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL %q: %w", baseURL, err)
	}
	switch u.Scheme {
	case "file":
		p := filepath.Join(u.Path, filepath.FromSlash(rel))
		return readFileLimited(p, maxBytes)
	case "http", "https":
		joined := strings.TrimSuffix(baseURL, "/") + "/" + strings.TrimPrefix(rel, "/")
		return httpGet(ctx, joined, timeout, maxBytes)
	default:
		return nil, fmt.Errorf("unsupported URL scheme %q (want https or file)", u.Scheme)
	}
}

func readFileLimited(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	data, err := readLimited(f, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

func httpGet(ctx context.Context, rawURL string, timeout time.Duration, maxBytes int64) ([]byte, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	reqCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: unexpected status %s", rawURL, resp.Status)
	}
	data, err := readLimited(resp.Body, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	return data, nil
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 30 // 1 GiB safety cap
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("content exceeds %d byte limit", maxBytes)
	}
	return data, nil
}
