package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"servercli/internal/oss"
)

type httpOSSProvider struct {
	cfg      oss.Config
	endpoint *url.URL
	client   *http.Client
	pathMode bool
}

func newHTTPOSSProvider(cfg oss.Config) (*httpOSSProvider, error) {
	cfg.Normalize()
	endpointValue := cfg.Endpoint
	if cfg.PreferInternal && cfg.InternalEndpoint != "" {
		endpointValue = cfg.InternalEndpoint
	}
	if endpointValue == "" || cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
		return nil, errors.New("OSS endpoint, bucket, access key ID, and access key secret are required")
	}
	if !strings.Contains(endpointValue, "://") {
		endpointValue = "https://" + endpointValue
	}
	endpoint, err := url.Parse(endpointValue)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, fmt.Errorf("invalid OSS endpoint %q", endpointValue)
	}
	if endpoint.Path != "" && endpoint.Path != "/" {
		return nil, errors.New("OSS endpoint must not contain a path")
	}
	hostname := endpoint.Hostname()
	pathMode := hostname == "localhost" || net.ParseIP(hostname) != nil
	if !pathMode && !strings.HasPrefix(strings.ToLower(hostname), strings.ToLower(cfg.Bucket)+".") {
		host := cfg.Bucket + "." + hostname
		if port := endpoint.Port(); port != "" {
			host += ":" + port
		}
		endpoint.Host = host
	}
	return &httpOSSProvider{cfg: cfg, endpoint: endpoint, client: &http.Client{Timeout: cfg.Timeout}, pathMode: pathMode}, nil
}

func (provider *httpOSSProvider) Put(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	resp, err := provider.request(ctx, http.MethodPut, key, data, contentType)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := provider.checkResponse(resp); err != nil {
		return "", err
	}
	return strings.Trim(resp.Header.Get("ETag"), `"`), nil
}

func (provider *httpOSSProvider) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := provider.request(ctx, http.MethodGet, key, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := provider.checkResponse(resp); err != nil {
		return nil, err
	}
	return io.ReadAll(resp.Body)
}

func (provider *httpOSSProvider) Head(ctx context.Context, key string) (*oss.ObjectMeta, error) {
	resp, err := provider.request(ctx, http.MethodHead, key, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := provider.checkResponse(resp); err != nil {
		return nil, err
	}
	meta := &oss.ObjectMeta{Key: key, ETag: strings.Trim(resp.Header.Get("ETag"), `"`), ContentType: resp.Header.Get("Content-Type")}
	meta.Size, _ = strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	meta.LastModified, _ = http.ParseTime(resp.Header.Get("Last-Modified"))
	return meta, nil
}

func (provider *httpOSSProvider) Exists(ctx context.Context, key string) (bool, error) {
	_, err := provider.Head(ctx, key)
	if errors.Is(err, oss.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (provider *httpOSSProvider) Delete(ctx context.Context, key string) error {
	resp, err := provider.request(ctx, http.MethodDelete, key, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return provider.checkResponse(resp)
}

func (provider *httpOSSProvider) PutVerified(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	if _, err := provider.Put(ctx, key, data, contentType); err != nil {
		return "", err
	}
	digest := oss.SHA256Hex(data)
	if _, err := provider.GetVerified(ctx, key, digest); err != nil {
		return "", err
	}
	return digest, nil
}

func (provider *httpOSSProvider) GetVerified(ctx context.Context, key, expectedSHA256 string) ([]byte, error) {
	data, err := provider.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if err := oss.VerifySHA256(data, expectedSHA256); err != nil {
		return nil, err
	}
	return data, nil
}

func (provider *httpOSSProvider) request(ctx context.Context, method, key string, data []byte, contentType string) (*http.Response, error) {
	key = strings.TrimLeft(key, "/")
	if key == "" {
		return nil, errors.New("OSS object key is required")
	}
	requestURL := *provider.endpoint
	requestPath := "/" + key
	rawRequestPath := "/" + escapeOSSKey(key)
	if provider.pathMode {
		requestPath = "/" + provider.cfg.Bucket + requestPath
		rawRequestPath = "/" + url.PathEscape(provider.cfg.Bucket) + rawRequestPath
	}
	requestURL.Path = requestPath
	requestURL.RawPath = rawRequestPath
	var body io.Reader
	if data != nil {
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	req.Header.Set("Date", date)
	if provider.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", provider.cfg.UserAgent)
	}
	canonicalResource := "/" + provider.cfg.Bucket + "/" + key
	stringToSign := method + "\n\n" + contentType + "\n" + date + "\n" + canonicalResource
	mac := hmac.New(sha1.New, []byte(provider.cfg.AccessKeySecret))
	_, _ = mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	req.Header.Set("Authorization", "OSS "+provider.cfg.AccessKeyID+":"+signature)
	return provider.client.Do(req)
}

func (provider *httpOSSProvider) checkResponse(resp *http.Response) error {
	if resp.StatusCode == http.StatusNotFound {
		return oss.ErrNotFound
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("OSS request returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

func escapeOSSKey(key string) string {
	parts := strings.Split(key, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
