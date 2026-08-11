package oss

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultUserAgent = "servercli-oss/1"
	maxErrorBody     = 1 << 20
)

// Client implements Provider using the Aliyun OSS REST API V1 signature.
// It is safe for concurrent use.
type Client struct {
	endpoint        *url.URL
	bucket          string
	accessKeyID     string
	accessKeySecret string
	retries         int
	userAgent       string
	httpClient      *http.Client
	baseBackoff     time.Duration
}

// NewClient constructs an OSS client. Config is normalized before use.
func NewClient(cfg Config) (*Client, error) {
	cfg.Normalize()

	endpoint := strings.TrimSpace(cfg.effectiveEndpoint())
	if endpoint == "" {
		return nil, errors.New("oss: endpoint is required")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("oss: bucket is required")
	}
	if strings.TrimSpace(cfg.AccessKeyID) == "" {
		return nil, errors.New("oss: access key id is required")
	}
	if cfg.AccessKeySecret == "" {
		return nil, errors.New("oss: access key secret is required")
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("oss: parse endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("oss: endpoint scheme must be http or https")
	}
	if u.Host == "" {
		return nil, errors.New("oss: endpoint host is required")
	}
	if u.User != nil {
		return nil, errors.New("oss: endpoint must not contain user information")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("oss: endpoint must not contain a query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""

	userAgent := cfg.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	return &Client{
		endpoint:        u,
		bucket:          strings.TrimSpace(cfg.Bucket),
		accessKeyID:     strings.TrimSpace(cfg.AccessKeyID),
		accessKeySecret: cfg.AccessKeySecret,
		retries:         cfg.Retries,
		userAgent:       userAgent,
		httpClient:      &http.Client{Timeout: cfg.Timeout},
		baseBackoff:     50 * time.Millisecond,
	}, nil
}

// New constructs an OSS Provider.
func New(cfg Config) (Provider, error) {
	return NewClient(cfg)
}

// Put uploads data to key and returns the response ETag when present.
func (c *Client) Put(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	sum := md5.Sum(data)
	headers := make(http.Header)
	headers.Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
	if contentType != "" {
		headers.Set("Content-Type", contentType)
	}

	resp, err := c.do(ctx, http.MethodPut, key, data, headers)
	if err != nil {
		return "", err
	}
	return cleanETag(resp.Header.Get("ETag")), nil
}

// Get downloads key.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.body, nil
}

// Head returns metadata for key without downloading its body.
func (c *Client) Head(ctx context.Context, key string) (*ObjectMeta, error) {
	resp, err := c.do(ctx, http.MethodHead, key, nil, nil)
	if err != nil {
		return nil, err
	}

	meta := &ObjectMeta{
		Key:         key,
		Size:        -1,
		ETag:        cleanETag(resp.Header.Get("ETag")),
		ContentType: resp.Header.Get("Content-Type"),
	}
	if value := resp.Header.Get("Content-Length"); value != "" {
		if size, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil {
			meta.Size = size
		}
	}
	if value := resp.Header.Get("Last-Modified"); value != "" {
		if modified, parseErr := http.ParseTime(value); parseErr == nil {
			meta.LastModified = modified
		}
	}
	return meta, nil
}

// Exists reports whether key exists.
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	_, err := c.Head(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Delete removes key. Deleting a missing object succeeds.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.do(ctx, http.MethodDelete, key, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// PutVerified uploads data, downloads it again, and verifies its SHA256 digest.
func (c *Client) PutVerified(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	digest := SHA256Hex(data)
	if _, err := c.Put(ctx, key, data, contentType); err != nil {
		return "", err
	}
	readBack, err := c.Get(ctx, key)
	if err != nil {
		return "", err
	}
	if err := VerifySHA256(readBack, digest); err != nil {
		return "", err
	}
	return digest, nil
}

// GetVerified downloads key and verifies expectedSHA256 before returning it.
func (c *Client) GetVerified(ctx context.Context, key string, expectedSHA256 string) ([]byte, error) {
	data, err := c.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if err := VerifySHA256(data, expectedSHA256); err != nil {
		return nil, err
	}
	return data, nil
}

type response struct {
	Header http.Header
	body   []byte
}

func (c *Client) do(ctx context.Context, method, key string, body []byte, headers http.Header) (*response, error) {
	requestURL, canonicalResource, err := c.objectURL(key)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			if err := sleepContext(ctx, c.backoff(attempt-1)); err != nil {
				return nil, err
			}
		}

		req, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("oss: create %s request: %w", method, err)
		}
		for name, values := range headers {
			for _, value := range values {
				req.Header.Add(name, value)
			}
		}
		if c.userAgent != "" {
			req.Header.Set("User-Agent", c.userAgent)
		}
		req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
		c.sign(req, canonicalResource)

		httpResp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("oss: %s request failed: %w", method, err)
			if attempt < c.retries {
				continue
			}
			return nil, lastErr
		}

		responseBody, readErr := readAndClose(httpResp.Body, method == http.MethodHead)
		if readErr != nil {
			lastErr = fmt.Errorf("oss: read %s response: %w", method, readErr)
			if attempt < c.retries {
				continue
			}
			return nil, lastErr
		}

		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			return &response{Header: httpResp.Header.Clone(), body: responseBody}, nil
		}
		if httpResp.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}

		lastErr = newStatusError(method, httpResp.StatusCode, responseBody)
		if isTransientStatus(httpResp.StatusCode) && attempt < c.retries {
			continue
		}
		return nil, lastErr
	}
	return nil, lastErr
}

func (c *Client) objectURL(key string) (string, string, error) {
	if key == "" {
		return "", "", errors.New("oss: object key is required")
	}

	u := *c.endpoint
	objectKey := strings.TrimPrefix(key, "/")
	if objectKey == "" {
		return "", "", errors.New("oss: object key is required")
	}
	basePath := strings.TrimRight(u.Path, "/")
	if usePathStyle(u.Hostname()) {
		u.Path = basePath + "/" + c.bucket + "/" + objectKey
	} else {
		if !strings.HasPrefix(strings.ToLower(u.Hostname()), strings.ToLower(c.bucket)+".") {
			u.Host = c.bucket + "." + u.Host
		}
		u.Path = basePath + "/" + objectKey
	}
	u.RawPath = ""

	canonicalResource := "/" + c.bucket + "/" + objectKey
	return u.String(), canonicalResource, nil
}

func usePathStyle(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return net.ParseIP(host) != nil
}

func (c *Client) sign(req *http.Request, canonicalResource string) {
	canonicalHeaders := canonicalOSSHeaders(req.Header)
	stringToSign := req.Method + "\n" +
		req.Header.Get("Content-MD5") + "\n" +
		req.Header.Get("Content-Type") + "\n" +
		req.Header.Get("Date") + "\n" +
		canonicalHeaders + canonicalResource

	mac := hmac.New(sha1.New, []byte(c.accessKeySecret))
	_, _ = mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	req.Header.Set("Authorization", "OSS "+c.accessKeyID+":"+signature)
}

func canonicalOSSHeaders(headers http.Header) string {
	type pair struct {
		name  string
		value string
	}
	var pairs []pair
	for name, values := range headers {
		lowerName := strings.ToLower(strings.TrimSpace(name))
		if !strings.HasPrefix(lowerName, "x-oss-") {
			continue
		}
		for i := range values {
			values[i] = strings.TrimSpace(values[i])
		}
		pairs = append(pairs, pair{name: lowerName, value: strings.Join(values, ",")})
	}
	if len(pairs) == 0 {
		return ""
	}
	// The client currently sets no x-oss-* headers, but keeping canonicalization
	// here makes signing correct if one is added later.
	for i := 1; i < len(pairs); i++ {
		for j := i; j > 0 && pairs[j].name < pairs[j-1].name; j-- {
			pairs[j], pairs[j-1] = pairs[j-1], pairs[j]
		}
	}
	var b strings.Builder
	for _, p := range pairs {
		b.WriteString(p.name)
		b.WriteByte(':')
		b.WriteString(p.value)
		b.WriteByte('\n')
	}
	return b.String()
}

func (c *Client) backoff(attempt int) time.Duration {
	delay := c.baseBackoff << min(attempt, 5)
	if delay > time.Second {
		return time.Second
	}
	return delay
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isTransientStatus(status int) bool {
	return status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests
}

func readAndClose(body io.ReadCloser, discard bool) ([]byte, error) {
	defer body.Close()
	if discard {
		_, err := io.Copy(io.Discard, body)
		return nil, err
	}
	return io.ReadAll(body)
}

func cleanETag(value string) string {
	return strings.Trim(strings.TrimSpace(value), "\"")
}

type serviceError struct {
	Code      string `xml:"Code"`
	RequestID string `xml:"RequestId"`
}

type statusError struct {
	method     string
	statusCode int
	code       string
	requestID  string
}

func newStatusError(method string, statusCode int, body []byte) error {
	var serviceErr serviceError
	if len(body) > maxErrorBody {
		body = body[:maxErrorBody]
	}
	_ = xml.Unmarshal(body, &serviceErr)
	return &statusError{
		method:     method,
		statusCode: statusCode,
		code:       serviceErr.Code,
		requestID:  serviceErr.RequestID,
	}
}

func (e *statusError) Error() string {
	parts := []string{"oss:", e.method, "failed with HTTP", strconv.Itoa(e.statusCode)}
	if e.code != "" {
		parts = append(parts, "("+e.code+")")
	}
	if e.requestID != "" {
		parts = append(parts, "request-id="+e.requestID)
	}
	return strings.Join(parts, " ")
}
