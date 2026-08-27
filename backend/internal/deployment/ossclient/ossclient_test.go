package ossclient

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testAK = "AKIDEXAMPLE"
	testSK = "S3cr3tKey!VaLuE"
)

// recorder collects handler-side check errors so that the test goroutine can
// assert on them after the client call completes (httptest handlers run on
// their own goroutines).
type recorder struct {
	mu   sync.Mutex
	errs []error
}

func (r *recorder) record(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

func (r *recorder) result() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// verifySignature reconstructs the OSS V1 StringToSign from the received
// request and compares the Authorization header against a locally computed
// signature. This validates the whole request/signing path end to end.
func verifySignature(r *http.Request, ak, secret string) error {
	auth := r.Header.Get("Authorization")
	wantPrefix := "OSS " + ak + ":"
	if !strings.HasPrefix(auth, wantPrefix) {
		return fmt.Errorf("Authorization %q does not start with %q", auth, wantPrefix)
	}
	cr := canonicalizedResourceFromRequest(r)
	ch := canonicalizedOSSHeaders(r.Header)
	sts := buildStringToSign(r.Method, r.Header.Get("Content-MD5"), r.Header.Get("Content-Type"), r.Header.Get("Date"), ch, cr)
	wantSig := sign(secret, sts)
	gotSig := strings.TrimPrefix(auth, wantPrefix)
	if gotSig != wantSig {
		return fmt.Errorf("signature mismatch: got %q want %q\nStringToSign:\n%q", gotSig, wantSig, sts)
	}
	return nil
}

// canonicalizedResourceFromRequest rebuilds the canonicalized resource from a
// received request path and query string.
func canonicalizedResourceFromRequest(r *http.Request) string {
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	idx := strings.Index(trimmed, "/")
	bucket, rest := trimmed, ""
	if idx >= 0 {
		bucket, rest = trimmed[:idx], trimmed[idx+1:]
	}
	cr := "/" + bucket + "/"
	if rest != "" {
		cr += rest
	}
	// OSS V1 签名：仅签名子资源参与；列表参数（prefix/delimiter/...）不参与
	if sub := signedSubResourceQuery(r.URL.Query()); sub != "" {
		cr += "?" + sub
	}
	return cr
}

func TestValidateObjectKey(t *testing.T) {
	valid := []string{
		"a",
		"a/b/c.txt",
		"file_1.tar.gz",
		"a.b-c/d_e",
		"dir/",
		strings.Repeat("x", 1024),
	}
	for _, k := range valid {
		if err := ValidateObjectKey(k); err != nil {
			t.Errorf("ValidateObjectKey(%q) unexpected error: %v", k, err)
		}
	}

	invalid := []string{
		"",                        // empty
		"../etc/passwd",           // path traversal
		"a/../b",                  // path traversal
		"a..b",                    // ".." anywhere
		"..",                      // exactly ".."
		"/leading",                // starts with /
		"a\\b",                    // backslash
		"a b",                     // space
		"a\tb",                    // tab (control)
		"a\nb",                    // newline (control)
		"a\x00b",                  // NUL (control)
		"a%b",                     // percent
		"a#b",                     // hash
		"a?b",                     // question mark
		"a*b",                     // asterisk
		"a:b",                     // colon
		"中文",                      // non-ASCII
		strings.Repeat("x", 1025), // too long
	}
	for _, k := range invalid {
		if err := ValidateObjectKey(k); err == nil {
			t.Errorf("ValidateObjectKey(%q) expected error", k)
		}
	}
}

func TestValidateBucket(t *testing.T) {
	valid := []string{
		"my-bucket",
		"abc",
		"a1b2c3",
		strings.Repeat("a", 63),
	}
	for _, b := range valid {
		if err := ValidateBucket(b); err != nil {
			t.Errorf("ValidateBucket(%q) unexpected error: %v", b, err)
		}
	}

	invalid := []string{
		"",
		"ab",                    // too short
		strings.Repeat("a", 64), // too long
		"My-Bucket",             // uppercase
		"my_bucket",             // underscore
		"bucket!",               // punctuation
		"bucket name",           // space
	}
	for _, b := range invalid {
		if err := ValidateBucket(b); err == nil {
			t.Errorf("ValidateBucket(%q) expected error", b)
		}
	}
}

// TestSignVectors checks the HMAC-SHA1 base64 signature against independently
// computed constants for fixed StringToSign inputs.
func TestSignVectors(t *testing.T) {
	const secret = "OtxrzxIsfpFjA7SwPzILwy8Bw21TLhquhboDYROV"
	cases := []struct {
		name string
		sts  string
		want string
	}{
		{
			name: "put object",
			sts:  "PUT\n\nimage/jpg\nTue, 27 Mar 2012 04:50:25 GMT\n/oss-example/nelson.jpg",
			want: "dqk+k1dUFN9AkDrtX2oTJQF2ELI=",
		},
		{
			name: "list with canonicalized headers and query",
			sts:  "GET\n\n\nFri, 24 Feb 2012 06:07:48 GMT\nx-oss-meta-owner:alice\nx-oss-process:image/resize\n/oss-example/?list-type=2&max-keys=100&prefix=prefix",
			want: "C1THRav9uLlVhyyi9COovNcPhV8=",
		},
		{
			name: "delete object",
			sts:  "DELETE\n\n\nWed, 01 Mar 2012 08:02:32 GMT\n/oss-example/nelson.jpg",
			want: "ipuPtN9CMvgocArH6aB8jL6wlms=",
		},
		{
			name: "head object",
			sts:  "HEAD\n\n\nWed, 01 Mar 2012 08:02:32 GMT\n/oss-example/nelson.jpg",
			want: "VGVSS/XpHSqSzCL+BM2um+vOZb0=",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sign(secret, tc.sts); got != tc.want {
				t.Errorf("sign() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildStringToSign(t *testing.T) {
	got := buildStringToSign("PUT", "", "image/jpg", "Tue, 27 Mar 2012 04:50:25 GMT", "", "/oss-example/nelson.jpg")
	want := "PUT\n\nimage/jpg\nTue, 27 Mar 2012 04:50:25 GMT\n/oss-example/nelson.jpg"
	if got != want {
		t.Errorf("buildStringToSign() = %q, want %q", got, want)
	}
}

func TestCanonicalizedOSSHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-OSS-Meta-Owner", "alice")
	h.Set("X-Oss-Process", " image/resize ")
	h.Set("Date", "ignored")
	h.Set("Content-Type", "ignored")
	h.Set("x-oss-tag", "tag1")
	got := canonicalizedOSSHeaders(h)
	want := "x-oss-meta-owner:alice\nx-oss-process:image/resize\nx-oss-tag:tag1\n"
	if got != want {
		t.Errorf("canonicalizedOSSHeaders() = %q, want %q", got, want)
	}
}

func TestCanonicalizedQuery(t *testing.T) {
	q := url.Values{}
	q.Set("prefix", "dir/sub")
	q.Set("list-type", "2")
	q.Set("continuation-token", "CgAAAAAABhYzNQ==")
	got := canonicalizedQuery(q)
	want := "continuation-token=CgAAAAAABhYzNQ==&list-type=2&prefix=dir/sub"
	if got != want {
		t.Errorf("canonicalizedQuery() = %q, want %q", got, want)
	}
}

func TestPutObject(t *testing.T) {
	var rec recorder
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(verifySignature(r, testAK, testSK))
		if r.Method != http.MethodPut {
			rec.record(fmt.Errorf("method = %s, want PUT", r.Method))
		}
		if r.URL.Path != "/my-bucket/dir/file.txt" {
			rec.record(fmt.Errorf("path = %q", r.URL.Path))
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "hello oss" {
			rec.record(fmt.Errorf("body = %q", body))
		}
		if r.ContentLength != int64(len("hello oss")) {
			rec.record(fmt.Errorf("ContentLength = %d", r.ContentLength))
		}
		if ct := r.Header.Get("Content-Type"); ct != "text/plain" {
			rec.record(fmt.Errorf("Content-Type = %q", ct))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newClient(ts.URL, Credentials{AccessKeyID: testAK, AccessKeySecret: testSK}, nil)
	payload := "hello oss"
	if err := c.PutObject(context.Background(), "my-bucket", "dir/file.txt", strings.NewReader(payload), int64(len(payload)), "text/plain"); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if err := rec.result(); err != nil {
		t.Fatal(err)
	}
}

// TestXOSSHeadersInSignature verifies that custom x-oss-* headers are
// canonicalized (lowercased, trimmed, sorted) into the signature.
func TestXOSSHeadersInSignature(t *testing.T) {
	var rec recorder
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(verifySignature(r, testAK, testSK))
		if got := canonicalizedOSSHeaders(r.Header); got != "x-oss-meta-owner:alice\nx-oss-process:image/resize\n" {
			rec.record(fmt.Errorf("unexpected canonicalized headers %q", got))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newClient(ts.URL, Credentials{AccessKeyID: testAK, AccessKeySecret: testSK}, nil)
	req, err := c.buildRequest(
		context.Background(),
		http.MethodPut,
		"my-bucket",
		"a.txt",
		"application/octet-stream",
		nil,
		map[string]string{"X-Oss-Meta-Owner": "alice", "x-oss-process": "image/resize"},
		strings.NewReader("data"),
		4,
	)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if err := rec.result(); err != nil {
		t.Fatal(err)
	}
}

func TestPutObject403ErrorParsing(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<Error>
  <Code>AccessDenied</Code>
  <Message>Access denied by authorizer's policy</Message>
  <RequestId>REQ12345</RequestId>
  <HostId>oss-cn-hangzhou.aliyuncs.com</HostId>
</Error>`)
	}))
	defer ts.Close()

	c := newClient(ts.URL, Credentials{AccessKeyID: testAK, AccessKeySecret: testSK}, nil)
	err := c.PutObject(context.Background(), "my-bucket", "a.txt", strings.NewReader("x"), 1, "")
	if err == nil {
		t.Fatal("expected error")
	}
	var oe *OSSError
	if !errors.As(err, &oe) {
		t.Fatalf("expected *OSSError, got %T: %v", err, err)
	}
	if oe.StatusCode != http.StatusForbidden {
		t.Errorf("StatusCode = %d", oe.StatusCode)
	}
	if oe.Code != "AccessDenied" {
		t.Errorf("Code = %q", oe.Code)
	}
	if oe.Message == "" {
		t.Error("Message must not be empty")
	}
	if oe.RequestID != "REQ12345" {
		t.Errorf("RequestID = %q", oe.RequestID)
	}
	if strings.Contains(err.Error(), testSK) || strings.Contains(err.Error(), testAK) {
		t.Error("error text must not contain credentials")
	}
}

func TestGetObject(t *testing.T) {
	var rec recorder
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(verifySignature(r, testAK, testSK))
		if r.Method != http.MethodGet {
			rec.record(fmt.Errorf("method = %s", r.Method))
		}
		if r.URL.Path != "/my-bucket/data.bin" {
			rec.record(fmt.Errorf("path = %q", r.URL.Path))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "payload-123")
	}))
	defer ts.Close()

	c := newClient(ts.URL, Credentials{AccessKeyID: testAK, AccessKeySecret: testSK}, nil)
	var buf bytes.Buffer
	n, err := c.GetObject(context.Background(), "my-bucket", "data.bin", &buf)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if n != int64(len("payload-123")) {
		t.Errorf("size = %d", n)
	}
	if buf.String() != "payload-123" {
		t.Errorf("content = %q", buf.String())
	}
	if err := rec.result(); err != nil {
		t.Fatal(err)
	}
}

func TestGetObject404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message><RequestId>REQ42</RequestId></Error>`)
	}))
	defer ts.Close()

	c := newClient(ts.URL, Credentials{AccessKeyID: testAK, AccessKeySecret: testSK}, nil)
	_, err := c.GetObject(context.Background(), "my-bucket", "missing.txt", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	var oe *OSSError
	if !errors.As(err, &oe) {
		t.Fatalf("expected *OSSError, got %T", err)
	}
	if oe.StatusCode != http.StatusNotFound || oe.Code != "NoSuchKey" {
		t.Errorf("got status=%d code=%q", oe.StatusCode, oe.Code)
	}
}

func TestHeadObject(t *testing.T) {
	var rec recorder
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(verifySignature(r, testAK, testSK))
		if r.Method != http.MethodHead {
			rec.record(fmt.Errorf("method = %s", r.Method))
		}
		w.Header().Set("Content-Length", "2048")
		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("Last-Modified", "Wed, 01 Mar 2012 08:02:32 GMT")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newClient(ts.URL, Credentials{AccessKeyID: testAK, AccessKeySecret: testSK}, nil)
	meta, err := c.HeadObject(context.Background(), "my-bucket", "data.bin")
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if meta.Size != 2048 {
		t.Errorf("Size = %d", meta.Size)
	}
	if meta.ETag != `"abc123"` {
		t.Errorf("ETag = %q", meta.ETag)
	}
	if meta.LastModified.IsZero() {
		t.Fatal("LastModified must be parsed")
	}
	if y := meta.LastModified.Year(); y != 2012 {
		t.Errorf("LastModified year = %d", y)
	}
	if err := rec.result(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteObject(t *testing.T) {
	var rec recorder
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(verifySignature(r, testAK, testSK))
		if r.Method != http.MethodDelete {
			rec.record(fmt.Errorf("method = %s", r.Method))
		}
		if r.URL.Path != "/my-bucket/gone.txt" {
			rec.record(fmt.Errorf("path = %q", r.URL.Path))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := newClient(ts.URL, Credentials{AccessKeyID: testAK, AccessKeySecret: testSK}, nil)
	if err := c.DeleteObject(context.Background(), "my-bucket", "gone.txt"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if err := rec.result(); err != nil {
		t.Fatal(err)
	}
}

func TestListObjectsPagination(t *testing.T) {
	const token = "CgAAAAAABhYzNQ=="
	var requests atomic.Int32
	var rec recorder
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		rec.record(verifySignature(r, testAK, testSK))
		if r.Method != http.MethodGet {
			rec.record(fmt.Errorf("method = %s", r.Method))
		}
		if r.URL.Path != "/my-bucket/" {
			rec.record(fmt.Errorf("path = %q", r.URL.Path))
		}
		q := r.URL.Query()
		if q.Get("list-type") != "2" {
			rec.record(fmt.Errorf("list-type = %q", q.Get("list-type")))
		}
		if q.Get("max-keys") != "1000" {
			rec.record(fmt.Errorf("max-keys = %q", q.Get("max-keys")))
		}
		if q.Get("prefix") != "dir/" {
			rec.record(fmt.Errorf("prefix = %q", q.Get("prefix")))
		}
		if q.Get("delimiter") != "/" {
			rec.record(fmt.Errorf("delimiter = %q", q.Get("delimiter")))
		}
		w.Header().Set("Content-Type", "application/xml")
		switch q.Get("continuation-token") {
		case "":
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult>
  <Name>my-bucket</Name>
  <Prefix>dir/</Prefix>
  <MaxKeys>1000</MaxKeys>
  <Delimiter>/</Delimiter>
  <IsTruncated>true</IsTruncated>
  <NextContinuationToken>`+token+`</NextContinuationToken>
  <Contents>
    <Key>dir/a.txt</Key>
    <LastModified>2023-01-01T08:00:00.000Z</LastModified>
    <ETag>"aaa"</ETag>
    <Size>10</Size>
  </Contents>
  <Contents>
    <Key>dir/b.txt</Key>
    <LastModified>2023-01-02T08:00:00.000Z</LastModified>
    <ETag>"bbb"</ETag>
    <Size>20</Size>
  </Contents>
</ListBucketResult>`)
		case token:
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult>
  <Name>my-bucket</Name>
  <Prefix>dir/</Prefix>
  <MaxKeys>1000</MaxKeys>
  <Delimiter>/</Delimiter>
  <IsTruncated>false</IsTruncated>
  <Contents>
    <Key>dir/c.txt</Key>
    <LastModified>2023-01-03T08:00:00.000Z</LastModified>
    <ETag>"ccc"</ETag>
    <Size>30</Size>
  </Contents>
  <CommonPrefixes>
    <Prefix>dir/sub/</Prefix>
  </CommonPrefixes>
</ListBucketResult>`)
		default:
			rec.record(fmt.Errorf("unexpected continuation-token %q", q.Get("continuation-token")))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer ts.Close()

	c := newClient(ts.URL, Credentials{AccessKeyID: testAK, AccessKeySecret: testSK}, nil)
	metas, err := c.ListObjects(context.Background(), "my-bucket", "dir/", "/")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if requests.Load() != 2 {
		t.Errorf("server received %d requests, want 2", requests.Load())
	}
	if len(metas) != 4 {
		t.Fatalf("len(metas) = %d, want 4: %+v", len(metas), metas)
	}
	want := []struct {
		key      string
		size     int64
		isPrefix bool
	}{
		{"dir/a.txt", 10, false},
		{"dir/b.txt", 20, false},
		{"dir/c.txt", 30, false},
		{"dir/sub/", 0, true},
	}
	for i, w := range want {
		if metas[i].Key != w.key || metas[i].Size != w.size || metas[i].IsPrefix != w.isPrefix {
			t.Errorf("meta[%d] = %+v, want key=%q size=%d isPrefix=%v", i, metas[i], w.key, w.size, w.isPrefix)
		}
	}
	if metas[0].LastModified.IsZero() {
		t.Error("LastModified should be parsed for contents")
	}
	if err := rec.result(); err != nil {
		t.Fatal(err)
	}
}

func TestNewEndpointValidation(t *testing.T) {
	creds := Credentials{AccessKeyID: testAK, AccessKeySecret: testSK}

	valid := []string{
		"https://oss-cn-hangzhou.aliyuncs.com",
		"https://oss-cn-hangzhou.aliyuncs.com/",
		"https://oss.aliyuncs.com.cn",
		"https://my-custom.aliyuncs.com",
		"https://aliyuncs.com",
	}
	for _, ep := range valid {
		if _, err := New(ep, creds); err != nil {
			t.Errorf("New(%q) unexpected error: %v", ep, err)
		}
	}

	invalid := []string{
		"",                                               // empty
		"http://oss-cn-hangzhou.aliyuncs.com",            // http
		"ftp://oss-cn-hangzhou.aliyuncs.com",             // wrong scheme
		"https://127.0.0.1",                              // IPv4
		"https://10.0.0.1",                               // private IPv4
		"https://[::1]",                                  // IPv6
		"https://localhost",                              // localhost
		"https://oss.example.com",                        // not allowlisted
		"https://evilaliyuncs.com",                       // suffix trick
		"https://aliyuncs.com.evil.com",                  // suffix trick
		"https://oss-cn-hangzhou.aliyuncs.com:443",       // port
		"https://user:pass@oss-cn-hangzhou.aliyuncs.com", // userinfo
		"https://oss-cn-hangzhou.aliyuncs.com/path",      // path
		"https://oss_cn.aliyuncs.com",                    // invalid host char
	}
	for _, ep := range invalid {
		if _, err := New(ep, creds); err == nil {
			t.Errorf("New(%q) expected error", ep)
		}
	}

	// Additional allowlisted host suffix via option.
	if _, err := New("https://oss.internal.example.com", creds, WithAllowlistHosts("example.com")); err != nil {
		t.Errorf("New with WithAllowlistHosts: %v", err)
	}
	// IP addresses must be rejected even when explicitly allowlisted.
	if _, err := New("https://127.0.0.1", creds, WithAllowlistHosts("127.0.0.1")); err == nil {
		t.Error("IP address must be rejected even when allowlisted")
	}
	// Empty credentials must be rejected.
	if _, err := New("https://oss-cn-hangzhou.aliyuncs.com", Credentials{}); err == nil {
		t.Error("empty credentials must be rejected")
	}
}

func TestNewWithTimeout(t *testing.T) {
	c, err := New("https://oss-cn-hangzhou.aliyuncs.com", Credentials{AccessKeyID: testAK, AccessKeySecret: testSK}, WithTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.httpClient.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v", c.httpClient.Timeout)
	}
}

// TestMethodsValidateBeforeRequest ensures that no request is sent to the
// server when bucket/object-key validation fails.
func TestMethodsValidateBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer ts.Close()

	c := newClient(ts.URL, Credentials{AccessKeyID: testAK, AccessKeySecret: testSK}, nil)
	ctx := context.Background()

	if err := c.PutObject(ctx, "My-Bucket", "a.txt", strings.NewReader("x"), 1, ""); err == nil {
		t.Error("PutObject with bad bucket: expected error")
	}
	if err := c.PutObject(ctx, "my-bucket", "/lead", strings.NewReader("x"), 1, ""); err == nil {
		t.Error("PutObject with key starting with '/': expected error")
	}
	if _, err := c.GetObject(ctx, "my-bucket", "../x", io.Discard); err == nil {
		t.Error("GetObject with traversal key: expected error")
	}
	if _, err := c.HeadObject(ctx, "my-bucket", "a\\b"); err == nil {
		t.Error("HeadObject with backslash key: expected error")
	}
	if err := c.DeleteObject(ctx, "my-bucket", ""); err == nil {
		t.Error("DeleteObject with empty key: expected error")
	}
	if _, err := c.ListObjects(ctx, "my-bucket", "..", ""); err == nil {
		t.Error("ListObjects with traversal prefix: expected error")
	}
	if calls.Load() != 0 {
		t.Errorf("server was called %d times, want 0", calls.Load())
	}
}

// TestVirtualHostedAddressing verifies that domain endpoints use virtual-hosted
// (third-level domain) addressing <bucket>.<endpoint>/<key> as required by OSS
// (path-style addressing returns SecondLevelDomainForbidden), while loopback
// (IP) endpoints keep path-style so httptest-based tests still work.
func TestVirtualHostedAddressing(t *testing.T) {
	var gotReq *http.Request
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotReq = r.Clone(r.Context())
		return &http.Response{
			StatusCode: 200,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)),
		}, nil
	})
	c, err := New("https://oss-cn-hangzhou.aliyuncs.com", Credentials{AccessKeyID: testAK, AccessKeySecret: testSK}, WithHTTPClient(&http.Client{Transport: rt}))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.ListObjects(context.Background(), "my-bucket", "servercli/", ""); err != nil {
		t.Fatalf("list: %v", err)
	}
	if gotReq == nil {
		t.Fatal("no request captured")
	}
	// 虚拟主机式：Host=bucket.endpoint，路径不含 bucket
	if gotReq.Host != "my-bucket.oss-cn-hangzhou.aliyuncs.com" {
		t.Errorf("Host = %q, want my-bucket.oss-cn-hangzhou.aliyuncs.com", gotReq.Host)
	}
	if gotReq.URL.Path != "/" {
		t.Errorf("URL path = %q, want /", gotReq.URL.Path)
	}
}

// roundTripFunc adapts a func to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }


// TestListObjectsSignatureMatchesSpecReference independently rebuilds the OSS
// V1 StringToSign for the exact ListObjects call used by the OSS Profile
// connection test (bucket + prefix + delimiter="/", list-type=2) and compares
// the Authorization header produced by the client against the spec reference.
// It deliberately avoids reusing the client's own helpers so a regression in
// canonicalizedQuery/canonicalizedResource cannot hide.
func TestListObjectsSignatureMatchesSpecReference(t *testing.T) {
	var got *http.Request
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.Clone(r.Context())
		return &http.Response{
			StatusCode: 200,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)),
		}, nil
	})
	const (
		ak = "LTAI-test-access-key-00000000"
		sk = "test-access-key-secret-0000000000000000"
	)
	c, err := New("https://oss-cn-hangzhou.aliyuncs.com", Credentials{AccessKeyID: ak, AccessKeySecret: sk},
		WithHTTPClient(&http.Client{Transport: rt}))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := c.ListObjects(context.Background(), "my-bucket", "deployment-repository/", "/"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got == nil {
		t.Fatal("no request captured")
	}
	if got.Host != "my-bucket.oss-cn-hangzhou.aliyuncs.com" {
		t.Fatalf("host = %q, want virtual-hosted", got.Host)
	}

	// 独立参考实现：按 OSS V1 规范重建 StringToSign。
	// canonicalized resource = /bucket/[object][?签名子资源]。列表类查询参数
	// （prefix/delimiter/marker/max-keys/list-type/continuation-token 等）不是
	// OSS 签名子资源，**不参与签名**（实测：OSS 返回的期望 StringToSign 只含
	// "/inori-tools/"）。本请求无签名子资源，故 canonicalized resource 恒为
	// /my-bucket/。
	canon := "/my-bucket/"
	if sub := signedSubResourceQuery(got.URL.Query()); sub != "" {
		canon += "?" + sub
	}
	sts := got.Method + "\n" +
		got.Header.Get("Content-MD5") + "\n" +
		got.Header.Get("Content-Type") + "\n" +
		got.Header.Get("Date") + "\n" +
		"" + // 无 x-oss-* 头
		canon

	mac := hmac.New(sha1.New, []byte(sk))
	mac.Write([]byte(sts))
	wantSig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	wantAuth := "OSS " + ak + ":" + wantSig

	if got.Header.Get("Authorization") != wantAuth {
		t.Errorf("Authorization mismatch\n got: %s\nwant: %s\nStringToSign:\n%q", got.Header.Get("Authorization"), wantAuth, sts)
	}
}
