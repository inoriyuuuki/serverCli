// Package ossclient implements a minimal Alibaba Cloud OSS client built
// exclusively on the Go standard library.
//
// It talks to OSS through the REST API with Signature Version 1
// (Authorization: OSS <AK>:<base64(HMAC-SHA1(secret, StringToSign))>).
// No third-party dependencies are used, and credentials are never logged,
// printed, or included in error text.
package ossclient

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// defaultTimeout is applied to the built-in HTTP client when the caller does
// not provide one via WithHTTPClient.
const defaultTimeout = 30 * time.Second

// defaultAllowlistHosts are the host suffixes permitted by New() unless
// extended via WithAllowlistHosts. Endpoints must live under Alibaba Cloud's
// OSS domain family; anything else (and any IP/private address) is rejected to
// prevent SSRF.
var defaultAllowlistHosts = []string{
	"aliyuncs.com",
	"aliyuncs.com.cn",
}

// Credentials holds the OSS access key pair. These values are secret: they
// must never be logged, printed, or surfaced in errors.
type Credentials struct {
	AccessKeyID     string
	AccessKeySecret string
}

// ObjectMeta describes a single OSS object (or a CommonPrefixes group when
// listing with a delimiter).
type ObjectMeta struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
	// IsPrefix is true when the entry was returned as a CommonPrefixes group
	// (i.e. a simulated directory produced by delimiter grouping). Size, ETag
	// and LastModified are zero for such entries.
	IsPrefix bool
}

// OSSError is returned for any non-2xx OSS response. It carries the HTTP
// status code and the OSS error code/message parsed from the XML error body.
type OSSError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *OSSError) Error() string {
	return fmt.Sprintf("oss error: status=%d code=%q message=%q", e.StatusCode, e.Code, e.Message)
}

// Client is a minimal OSS client. Create one with New.
type Client struct {
	endpoint   string
	creds      Credentials
	httpClient *http.Client
	// allowSuffixes records the endpoint host suffixes accepted by New.
	allowSuffixes []string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the HTTP client used for requests. The timeout of
// the supplied client is kept; if it is zero, the default timeout is applied.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithTimeout sets the timeout on the client's HTTP client.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if c.httpClient == nil {
			c.httpClient = &http.Client{}
		}
		c.httpClient.Timeout = d
	}
}

// WithAllowlistHosts extends the set of allowed endpoint host suffixes beyond
// the default *.aliyuncs.com / *.aliyuncs.com.cn family. Suffixes are matched
// on whole label boundaries (host == suffix or host ends with "."+suffix).
// IP addresses and non-https endpoints are always rejected.
func WithAllowlistHosts(hosts ...string) Option {
	return func(c *Client) { c.allowSuffixes = append(c.allowSuffixes, hosts...) }
}

// New creates a Client for the given HTTPS endpoint and credentials.
//
// The endpoint must:
//   - use the https scheme (http is rejected),
//   - not be an IP address (neither IPv4 nor IPv6), a localhost name, or an
//     internal/private address,
//   - be a hostname ending in an allowlisted suffix (default: *.aliyuncs.com
//     and *.aliyuncs.com.cn), and
//   - contain no port, path, query, fragment, or userinfo.
//
// Additional host suffixes can be permitted with WithAllowlistHosts.
func New(endpoint string, creds Credentials, opts ...Option) (*Client, error) {
	if creds.AccessKeyID == "" {
		return nil, errors.New("ossclient: AccessKeyID must not be empty")
	}
	if creds.AccessKeySecret == "" {
		return nil, errors.New("ossclient: AccessKeySecret must not be empty")
	}
	c := &Client{
		endpoint:      strings.TrimRight(endpoint, "/"),
		creds:         creds,
		httpClient:    &http.Client{Timeout: defaultTimeout},
		allowSuffixes: append([]string{}, defaultAllowlistHosts...),
	}
	for _, o := range opts {
		o(c)
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{}
	}
	if c.httpClient.Timeout <= 0 {
		c.httpClient.Timeout = defaultTimeout
	}
	applyRedirectGuard(c.httpClient, endpoint)
	if err := validateEndpoint(endpoint, c.allowSuffixes); err != nil {
		return nil, err
	}
	return c, nil
}

// newClient constructs a Client without endpoint validation. It is used by
// tests that need to point the client at a local httptest server; production
// code should always go through New.
func newClient(endpoint string, creds Credentials, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	if httpClient.Timeout <= 0 {
		httpClient.Timeout = defaultTimeout
	}
	applyRedirectGuard(httpClient, endpoint)
	return &Client{
		endpoint:   strings.TrimRight(endpoint, "/"),
		creds:      creds,
		httpClient: httpClient,
	}
}

// applyRedirectGuard installs a redirect policy on hc that only permits
// redirects staying on the same host over https. Cross-host or plain-http
// redirects are rejected to keep credentials from ever being replayed to a
// different origin.
func applyRedirectGuard(hc *http.Client, endpoint string) {
	if hc == nil {
		return
	}
	host := ""
	if u, err := url.Parse(strings.TrimRight(endpoint, "/")); err == nil {
		host = u.Hostname()
	}
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("ossclient: redirect to non-https scheme %q rejected", req.URL.Scheme)
		}
		if host != "" && !strings.EqualFold(req.URL.Hostname(), host) {
			return fmt.Errorf("ossclient: redirect to different host %q rejected", req.URL.Hostname())
		}
		return nil
	}
}

// validateEndpoint enforces the SSRF guard for New.
func validateEndpoint(endpoint string, suffixes []string) error {
	if endpoint == "" {
		return errors.New("ossclient: endpoint must not be empty")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("ossclient: invalid endpoint: %w", err)
	}
	if u.Scheme != "https" {
		return errors.New("ossclient: endpoint must use https")
	}
	if u.User != nil {
		return errors.New("ossclient: endpoint must not contain userinfo")
	}
	if u.Port() != "" {
		return errors.New("ossclient: endpoint must not contain a port")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("ossclient: endpoint must not contain a path")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("ossclient: endpoint must not contain query or fragment")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("ossclient: endpoint missing host")
	}
	if net.ParseIP(host) != nil {
		return errors.New("ossclient: endpoint must not be an IP address")
	}
	if !validHostChars(host) {
		return errors.New("ossclient: endpoint host contains invalid characters")
	}
	if !isAllowlistedHost(host, suffixes) {
		return fmt.Errorf("ossclient: endpoint host %q is not allowlisted", host)
	}
	return nil
}

func validHostChars(host string) bool {
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func isAllowlistedHost(host string, suffixes []string) bool {
	h := strings.ToLower(host)
	for _, s := range suffixes {
		s = strings.ToLower(s)
		if h == s || strings.HasSuffix(h, "."+s) {
			return true
		}
	}
	return false
}

// ValidateBucket validates an OSS bucket name. Bucket names may only contain
// lowercase letters, digits, and hyphens, and must be 3-63 characters long.
func ValidateBucket(name string) error {
	if len(name) < 3 || len(name) > 63 {
		return fmt.Errorf("ossclient: bucket name must be 3-63 characters long, got %d", len(name))
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return fmt.Errorf("ossclient: bucket name %q contains invalid character %q (only lowercase letters, digits and hyphens are allowed)", name, r)
		}
	}
	return nil
}

// ValidateObjectKey validates an OSS object key.
//
// Allowed keys are non-empty, at most 1024 bytes, must not start with '/',
// must not contain ".." (which also forbids "../"), and may only contain the
// characters [a-zA-Z0-9/._-]. This keeps object keys safe to place in a URL
// path and prevents path traversal / key-escape attacks.
func ValidateObjectKey(key string) error {
	if key == "" {
		return errors.New("ossclient: object key must not be empty")
	}
	if len(key) > 1024 {
		return fmt.Errorf("ossclient: object key too long: %d bytes (max 1024)", len(key))
	}
	if key[0] == '/' {
		return errors.New("ossclient: object key must not start with '/'")
	}
	if strings.Contains(key, "..") {
		return errors.New("ossclient: object key must not contain '..'")
	}
	for _, r := range key {
		if !isAllowedObjectKeyRune(r) {
			return fmt.Errorf("ossclient: object key %q contains invalid character %q", key, r)
		}
	}
	return nil
}

func isAllowedObjectKeyRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		r == '/' || r == '.' || r == '_' || r == '-'
}

// validateDelimiter validates a ListObjects delimiter. Unlike an object key, a
// delimiter may legitimately be "/" (the standard directory delimiter), so it
// is not subject to the "must not start with '/'" rule. It still blocks path
// traversal (".."), backslashes, control characters, and anything outside the
// safe object-key character set.
func validateDelimiter(d string) error {
	if d == "" {
		return nil
	}
	if strings.Contains(d, "..") {
		return errors.New("ossclient: delimiter must not contain '..'")
	}
	for _, r := range d {
		if !isAllowedObjectKeyRune(r) {
			return fmt.Errorf("ossclient: delimiter %q contains invalid character %q", d, r)
		}
	}
	return nil
}

// PutObject uploads an object. The size must match the number of bytes
// readable from r. When contentType is non-empty it is sent as the
// Content-Type header and included in the signature.
func (c *Client) PutObject(ctx context.Context, bucket, objectKey string, r io.Reader, size int64, contentType string) error {
	if err := ValidateBucket(bucket); err != nil {
		return err
	}
	if err := ValidateObjectKey(objectKey); err != nil {
		return err
	}
	if size < 0 {
		return errors.New("ossclient: size must not be negative")
	}
	req, err := c.buildRequest(ctx, http.MethodPut, bucket, objectKey, contentType, nil, nil, r, size)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !isSuccess(resp.StatusCode) {
		return readError(resp)
	}
	// Drain a small amount so the connection can be reused; OSS returns an
	// empty body for successful writes.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return nil
}

// GetObject downloads an object and writes its body to w, returning the number
// of bytes written.
func (c *Client) GetObject(ctx context.Context, bucket, objectKey string, w io.Writer) (int64, error) {
	if err := ValidateBucket(bucket); err != nil {
		return 0, err
	}
	if err := ValidateObjectKey(objectKey); err != nil {
		return 0, err
	}
	req, err := c.buildRequest(ctx, http.MethodGet, bucket, objectKey, "", nil, nil, nil, 0)
	if err != nil {
		return 0, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if !isSuccess(resp.StatusCode) {
		return 0, readError(resp)
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return n, fmt.Errorf("ossclient: read object body: %w", err)
	}
	return n, nil
}

// HeadObject fetches object metadata without downloading the body.
func (c *Client) HeadObject(ctx context.Context, bucket, objectKey string) (*ObjectMeta, error) {
	if err := ValidateBucket(bucket); err != nil {
		return nil, err
	}
	if err := ValidateObjectKey(objectKey); err != nil {
		return nil, err
	}
	req, err := c.buildRequest(ctx, http.MethodHead, bucket, objectKey, "", nil, nil, nil, 0)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if !isSuccess(resp.StatusCode) {
		return nil, readError(resp)
	}
	meta := &ObjectMeta{Key: objectKey, Size: resp.ContentLength}
	if meta.Size < 0 {
		meta.Size = 0
	}
	meta.ETag = resp.Header.Get("ETag")
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, err := http.ParseTime(lm); err == nil {
			meta.LastModified = t
		}
	}
	return meta, nil
}

// DeleteObject deletes an object. Deleting a non-existent object is not an
// error for OSS.
func (c *Client) DeleteObject(ctx context.Context, bucket, objectKey string) error {
	if err := ValidateBucket(bucket); err != nil {
		return err
	}
	if err := ValidateObjectKey(objectKey); err != nil {
		return err
	}
	req, err := c.buildRequest(ctx, http.MethodDelete, bucket, objectKey, "", nil, nil, nil, 0)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !isSuccess(resp.StatusCode) {
		return readError(resp)
	}
	return nil
}

// ListObjects lists objects under bucket using the OSS ListObjectsV2 API
// (list-type=2). It automatically follows continuation-token pagination (at
// most 1000 keys per page) and returns object entries plus any CommonPrefixes
// groups produced by delimiter. A non-empty prefix is subject to the same
// validation as an object key.
func (c *Client) ListObjects(ctx context.Context, bucket, prefix, delimiter string) ([]ObjectMeta, error) {
	if err := ValidateBucket(bucket); err != nil {
		return nil, err
	}
	if prefix != "" {
		if err := ValidateObjectKey(prefix); err != nil {
			return nil, err
		}
	}
	if delimiter != "" {
		if err := validateDelimiter(delimiter); err != nil {
			return nil, err
		}
	}

	var metas []ObjectMeta
	continuationToken := ""
	// Defensive cap so a broken or hostile server cannot make us loop forever.
	const maxPages = 10000
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, errors.New("ossclient: list objects exceeded maximum number of pages")
		}
		q := url.Values{}
		q.Set("list-type", "2")
		q.Set("max-keys", "1000")
		if prefix != "" {
			q.Set("prefix", prefix)
		}
		if delimiter != "" {
			q.Set("delimiter", delimiter)
		}
		if continuationToken != "" {
			q.Set("continuation-token", continuationToken)
		}

		req, err := c.buildRequest(ctx, http.MethodGet, bucket, "", "", q, nil, nil, 0)
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if !isSuccess(resp.StatusCode) {
			return nil, parseOSSError(resp.StatusCode, resp.Status, string(body))
		}

		var result listBucketResult
		if err := xml.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("ossclient: decode list response: %w", err)
		}
		for _, c := range result.Contents {
			metas = append(metas, ObjectMeta{
				Key:          c.Key,
				Size:         c.Size,
				ETag:         c.ETag,
				LastModified: parseOSSDate(c.LastModified),
			})
		}
		for _, cp := range result.CommonPrefixes {
			metas = append(metas, ObjectMeta{Key: cp.Prefix, IsPrefix: true})
		}

		if !result.IsTruncated || result.NextContinuationToken == "" {
			break
		}
		continuationToken = result.NextContinuationToken
	}
	return metas, nil
}

// buildRequest constructs a signed OSS REST request. body may be nil. query
// parameters (if any) are included both in the request URL and in the
// canonicalized resource for signing. headers holds extra request headers
// (e.g. x-oss-* headers), which are canonicalized into the signature.
func (c *Client) buildRequest(ctx context.Context, method, bucket, objectKey, contentType string, query url.Values, headers map[string]string, body io.Reader, size int64) (*http.Request, error) {
	if err := ValidateBucket(bucket); err != nil {
		return nil, err
	}
	if objectKey != "" {
		if err := ValidateObjectKey(objectKey); err != nil {
			return nil, err
		}
	}

	// OSS 要求虚拟主机式（三级域名）寻址 <bucket>.<endpoint>/<key>；路径式
	// <endpoint>/<bucket>/<key>（二级域名）会被拒绝（SecondLevelDomainForbidden）。
	// 真实域名 endpoint 一律用虚拟主机式；仅测试/回环（IP/localhost）保留路径式。
	u, err := url.Parse(c.endpoint)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	virtualHosted := net.ParseIP(host) == nil && !strings.HasPrefix(strings.ToLower(host), "localhost")
	if virtualHosted {
		if p := u.Port(); p != "" {
			u.Host = bucket + "." + u.Hostname() + ":" + p
		} else {
			u.Host = bucket + "." + u.Hostname()
		}
	}
	if objectKey != "" {
		if virtualHosted {
			u.Path = "/" + escapeObjectPath(objectKey)
		} else {
			u.Path = "/" + bucket + "/" + escapeObjectPath(objectKey)
		}
	} else {
		// Bucket-level operation (ListObjects): keep the trailing slash so the
		// URL path matches the canonicalized resource.
		if virtualHosted {
			u.Path = "/"
		} else {
			u.Path = "/" + bucket + "/"
		}
	}
	u.RawQuery = query.Encode()
	u2 := u.String()

	// Build the request without a body so net/http never sniffs a
	// Content-Type; the body is attached afterwards so the signature's
	// Content-Type line stays exactly under our control.
	req, err := http.NewRequestWithContext(ctx, method, u2, nil)
	if err != nil {
		return nil, err
	}
	if body != nil && size > 0 {
		req.Body = io.NopCloser(body)
	}
	req.ContentLength = size

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	req.Header.Set("Date", date)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// OSS V1 签名：canonicalized resource 只包含真正的"子资源"（sub-resource）
	// 查询参数（如 ?acl、?uploads、?uploadId、?response-content-* 等）。普通
	// 列表参数（prefix/delimiter/marker/max-keys/list-type/continuation-token/
	// encoding-type/fetch-owner/start-after 等）不参与签名——把它们加进去会
	// 导致 SignatureDoesNotMatch。本客户端当前操作不使用任何签名子资源，
	// 因此 canonicalized resource 恒为 /bucket/object；仍保留子资源过滤逻辑
	// 以便未来扩展 multipart 等操作。
	canonicalizedResource := "/" + bucket + "/" + objectKey
	if objectKey == "" {
		canonicalizedResource = "/" + bucket + "/"
	}
	if sub := signedSubResourceQuery(query); sub != "" {
		canonicalizedResource += "?" + sub
	}
	canonicalizedHeaders := canonicalizedOSSHeaders(req.Header)
	stringToSign := buildStringToSign(
		method,
		req.Header.Get("Content-MD5"),
		req.Header.Get("Content-Type"),
		date,
		canonicalizedHeaders,
		canonicalizedResource,
	)
	signature := sign(c.creds.AccessKeySecret, stringToSign)
	req.Header.Set("Authorization", "OSS "+c.creds.AccessKeyID+":"+signature)
	return req, nil
}

// buildStringToSign assembles the OSS Signature Version 1 StringToSign.
func buildStringToSign(method, contentMD5, contentType, date, canonicalizedOSSHeaders, canonicalizedResource string) string {
	return method + "\n" + contentMD5 + "\n" + contentType + "\n" + date + "\n" +
		canonicalizedOSSHeaders + canonicalizedResource
}

// sign computes base64(HMAC-SHA1(secret, stringToSign)).
func sign(secret, stringToSign string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// canonicalizedOSSHeaders serializes x-oss-* request headers in the form
// "x-oss-name:value\n", lowercased, trimmed, and sorted by name, as required
// by the OSS V1 signature algorithm.
func canonicalizedOSSHeaders(h http.Header) string {
	var keys []string
	for k := range h {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-oss-") {
			keys = append(keys, lk)
		}
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(":")
		sb.WriteString(strings.TrimSpace(h.Get(k)))
		sb.WriteString("\n")
	}
	return sb.String()
}

// canonicalizedQuery serializes query parameters as "k1=v1&k2=v2" sorted by
// key with raw (URL-decoded) values, matching how OSS includes sub-resources
// in the canonicalized resource.
// signedSubResourceQuery returns the canonicalized query string for ONLY the
// query parameters that OSS treats as signed sub-resources. All other query
// parameters (list/prefix/delimiter/marker/max-keys/list-type/continuation-token
// and friends) are deliberately excluded from the OSS V1 signature; including
// them yields SignatureDoesNotMatch. Returns "" when no signed sub-resource is
// present.
func signedSubResourceQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		if isSignedSubResource(k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		if v := q.Get(k); v != "" {
			sb.WriteByte('=')
			sb.WriteString(v)
		}
	}
	return sb.String()
}

// isSignedSubResource reports whether k is an OSS sub-resource whose query
// value is included in the V1 signature (see Alibaba Cloud OSS docs).
func isSignedSubResource(k string) bool {
	switch k {
	case "acl", "append", "callback", "callback-var", "cors", "delete",
		"encryption", "lifecycle", "location", "logging", "metadata",
		"notification", "partNumber", "policy", "progress", "qos", "referer",
		"replication", "requestPayment", "restore", "response-cache-control",
		"response-content-disposition", "response-content-encoding",
		"response-content-language", "response-content-type", "response-expires",
		"sequence", "symlink", "tagging", "torrent", "uploadId", "uploads",
		"versionId", "versioning", "versions", "website":
		return true
	default:
		return false
	}
}

func canonicalizedQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(q.Get(k))
	}
	return sb.String()
}

// escapeObjectPath percent-escapes each path segment of an object key while
// preserving the "/" separators. Given the restricted object-key charset this
// is mostly a no-op, but it keeps the URL construction robust.
func escapeObjectPath(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// parseOSSDate parses the OSS LastModified timestamp format
// (e.g. "2023-01-01T08:00:00.000Z"). It returns the zero time on failure.
func parseOSSDate(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

func isSuccess(code int) bool {
	return code >= 200 && code < 300
}

// readError consumes the (bounded) error body and builds an *OSSError.
func readError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return parseOSSError(resp.StatusCode, resp.Status, string(body))
}

// parseOSSError parses an OSS XML error body (<Error><Code>/<Message>) into an
// *OSSError. If the body is not XML, the HTTP status text is used as the code.
func parseOSSError(statusCode int, status, body string) error {
	var oe ossErrorBody
	if err := xml.Unmarshal([]byte(body), &oe); err == nil && oe.Code != "" {
		return &OSSError{StatusCode: statusCode, Code: oe.Code, Message: oe.Message, RequestID: oe.RequestID}
	}
	msg := strings.TrimSpace(body)
	if msg == "" {
		msg = status
	}
	return &OSSError{StatusCode: statusCode, Code: status, Message: msg}
}

type ossErrorBody struct {
	Code      string `xml:"Code"`
	Message   string `xml:"Message"`
	RequestID string `xml:"RequestId"`
}

// listBucketResult is the ListObjectsV2 (list-type=2) response envelope.
type listBucketResult struct {
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	ContinuationToken     string         `xml:"ContinuationToken"`
	NextContinuationToken string         `xml:"NextContinuationToken"`
	KeyCount              int            `xml:"KeyCount"`
	MaxKeys               int            `xml:"MaxKeys"`
	Delimiter             string         `xml:"Delimiter"`
	IsTruncated           bool           `xml:"IsTruncated"`
	Contents              []objectEntry  `xml:"Contents"`
	CommonPrefixes        []commonPrefix `xml:"CommonPrefixes"`
}

type objectEntry struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

type commonPrefix struct {
	Prefix string `xml:"Prefix"`
}
