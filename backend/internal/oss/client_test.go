package oss

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testBucket = "test-bucket"
	testAK     = "test-access-key"
	testSecret = "super-secret-that-must-not-leak"
)

type fakeObject struct {
	data         []byte
	contentType  string
	etag         string
	lastModified time.Time
}

type fakeOSS struct {
	mu sync.Mutex

	objects       map[string]fakeObject
	failRemaining map[string]int
	requestCounts map[string]int
	getCounts     map[string]int
	corruptGET    map[string]bool

	authErrors []string
	leaks      []string
}

func newFakeOSS() *fakeOSS {
	return &fakeOSS{
		objects:       make(map[string]fakeObject),
		failRemaining: make(map[string]int),
		requestCounts: make(map[string]int),
		getCounts:     make(map[string]int),
		corruptGET:    make(map[string]bool),
	}
}

func (f *fakeOSS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusInternalServerError)
		return
	}

	key, ok := strings.CutPrefix(r.URL.Path, "/"+testBucket+"/")
	requestID := r.Method + " " + r.URL.Path

	f.mu.Lock()
	defer f.mu.Unlock()

	f.requestCounts[requestID]++
	if r.Method == http.MethodGet && ok {
		f.getCounts[key]++
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "OSS "+testAK+":") {
		f.authErrors = append(f.authErrors, r.Header.Get("Authorization"))
	}
	if strings.Contains(r.URL.RequestURI(), testSecret) {
		f.leaks = append(f.leaks, "URL: "+r.URL.RequestURI())
	}
	if strings.Contains(string(body), testSecret) {
		f.leaks = append(f.leaks, "body for "+requestID)
	}
	for name, values := range r.Header {
		if strings.EqualFold(name, "Authorization") {
			continue
		}
		if strings.Contains(strings.Join(values, ","), testSecret) {
			f.leaks = append(f.leaks, "header "+name)
		}
	}

	if !ok || key == "" {
		http.Error(w, "bad object path", http.StatusBadRequest)
		return
	}
	if f.failRemaining[requestID] > 0 {
		f.failRemaining[requestID]--
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `<Error><Code>InternalError</Code><RequestId>retry-test</RequestId></Error>`)
		return
	}

	switch r.Method {
	case http.MethodPut:
		if value := r.Header.Get("Content-MD5"); value != "" {
			sum := md5.Sum(body)
			if value != base64.StdEncoding.EncodeToString(sum[:]) {
				http.Error(w, "bad content md5", http.StatusBadRequest)
				return
			}
		}
		sum := md5.Sum(body)
		etag := hex.EncodeToString(sum[:])
		f.objects[key] = fakeObject{
			data:         append([]byte(nil), body...),
			contentType:  r.Header.Get("Content-Type"),
			etag:         etag,
			lastModified: time.Now().UTC().Truncate(time.Second),
		}
		w.Header().Set("ETag", `"`+etag+`"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		object, exists := f.objects[key]
		if !exists {
			writeNotFound(w)
			return
		}
		w.Header().Set("Content-Type", object.contentType)
		w.Header().Set("ETag", `"`+object.etag+`"`)
		data := append([]byte(nil), object.data...)
		if f.corruptGET[key] {
			data = append(data, byte('!'))
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case http.MethodHead:
		object, exists := f.objects[key]
		if !exists {
			writeNotFound(w)
			return
		}
		w.Header().Set("Content-Type", object.contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(object.data)))
		w.Header().Set("ETag", `"`+object.etag+`"`)
		w.Header().Set("Last-Modified", object.lastModified.Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		if _, exists := f.objects[key]; !exists {
			writeNotFound(w)
			return
		}
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writeNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code><RequestId>missing-test</RequestId></Error>`)
}

func (f *fakeOSS) assertClean(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.authErrors) != 0 {
		t.Fatalf("requests with invalid Authorization prefix: %q", f.authErrors)
	}
	if len(f.leaks) != 0 {
		t.Fatalf("access key secret leaked outside Authorization: %q", f.leaks)
	}
}

func (f *fakeOSS) count(method, key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requestCounts[method+" /"+testBucket+"/"+key]
}

func (f *fakeOSS) getCount(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCounts[key]
}

func newTestClient(t *testing.T, endpoint string, mutate func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		Endpoint:        endpoint,
		Bucket:          testBucket,
		AccessKeyID:     testAK,
		AccessKeySecret: testSecret,
		Timeout:         2 * time.Second,
		Retries:         2,
		UserAgent:       "servercli-oss-test",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.baseBackoff = time.Millisecond
	return client
}

func TestClientPutGetRoundTrip(t *testing.T) {
	fake := newFakeOSS()
	server := httptest.NewServer(fake)
	defer server.Close()
	client := newTestClient(t, server.URL, nil)

	data := []byte("hello OSS")
	etag, err := client.Put(context.Background(), "round-trip/object.txt", data, "text/plain")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if etag == "" {
		t.Fatal("Put returned an empty ETag")
	}
	got, err := client.Get(context.Background(), "round-trip/object.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("Get data = %q, want %q", got, data)
	}
	fake.assertClean(t)
}

func TestClientHeadMetadata(t *testing.T) {
	fake := newFakeOSS()
	server := httptest.NewServer(fake)
	defer server.Close()
	client := newTestClient(t, server.URL, nil)

	data := []byte("metadata")
	etag, err := client.Put(context.Background(), "meta.bin", data, "application/octet-stream")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	meta, err := client.Head(context.Background(), "meta.bin")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if meta.Key != "meta.bin" || meta.Size != int64(len(data)) || meta.ETag != etag || meta.ContentType != "application/octet-stream" {
		t.Fatalf("Head metadata = %+v", meta)
	}
	if meta.LastModified.IsZero() {
		t.Fatal("Head LastModified is zero")
	}
	fake.assertClean(t)
}

func TestClientExists(t *testing.T) {
	fake := newFakeOSS()
	server := httptest.NewServer(fake)
	defer server.Close()
	client := newTestClient(t, server.URL, nil)

	if _, err := client.Put(context.Background(), "present", []byte("yes"), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for _, tc := range []struct {
		name string
		key  string
		want bool
	}{
		{name: "present", key: "present", want: true},
		{name: "missing", key: "missing", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := client.Exists(context.Background(), tc.key)
			if err != nil {
				t.Fatalf("Exists: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Exists(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
	fake.assertClean(t)
}

func TestClientDelete(t *testing.T) {
	fake := newFakeOSS()
	server := httptest.NewServer(fake)
	defer server.Close()
	client := newTestClient(t, server.URL, nil)

	if _, err := client.Put(context.Background(), "delete-me", []byte("gone"), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := client.Delete(context.Background(), "delete-me"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := client.Delete(context.Background(), "delete-me"); err != nil {
		t.Fatalf("idempotent Delete: %v", err)
	}
	if _, err := client.Get(context.Background(), "delete-me"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete error = %v, want ErrNotFound", err)
	}
	fake.assertClean(t)
}

func TestClientPutVerified(t *testing.T) {
	for _, tc := range []struct {
		name      string
		corrupt   bool
		wantError bool
	}{
		{name: "verified", corrupt: false, wantError: false},
		{name: "read-back mismatch", corrupt: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeOSS()
			server := httptest.NewServer(fake)
			defer server.Close()
			client := newTestClient(t, server.URL, nil)
			key := "verified.bin"
			fake.corruptGET[key] = tc.corrupt
			data := []byte("verify this payload")

			digest, err := client.PutVerified(context.Background(), key, data, "application/octet-stream")
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
					t.Fatalf("PutVerified error = %v, want sha256 mismatch", err)
				}
			} else {
				if err != nil {
					t.Fatalf("PutVerified: %v", err)
				}
				if digest != SHA256Hex(data) {
					t.Fatalf("digest = %q, want %q", digest, SHA256Hex(data))
				}
			}
			if got := fake.getCount(key); got != 1 {
				t.Fatalf("read-back GET count = %d, want 1", got)
			}
			fake.assertClean(t)
		})
	}
}

func TestClientGetVerified(t *testing.T) {
	fake := newFakeOSS()
	server := httptest.NewServer(fake)
	defer server.Close()
	client := newTestClient(t, server.URL, nil)
	data := []byte("download verification")
	if _, err := client.Put(context.Background(), "get-verified", data, "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	for _, tc := range []struct {
		name    string
		digest  string
		wantErr bool
	}{
		{name: "success", digest: SHA256Hex(data)},
		{name: "mismatch", digest: SHA256Hex([]byte("different")), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := client.GetVerified(context.Background(), "get-verified", tc.digest)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
					t.Fatalf("GetVerified error = %v, want sha256 mismatch", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetVerified: %v", err)
			}
			if string(got) != string(data) {
				t.Fatalf("GetVerified data = %q, want %q", got, data)
			}
		})
	}
	fake.assertClean(t)
}

func TestClientRetriesTransientFailureOnce(t *testing.T) {
	fake := newFakeOSS()
	key := "retry-once"
	fake.failRemaining[http.MethodPut+" /"+testBucket+"/"+key] = 1
	server := httptest.NewServer(fake)
	defer server.Close()
	client := newTestClient(t, server.URL, func(cfg *Config) { cfg.Retries = 1 })

	if _, err := client.Put(context.Background(), key, []byte("eventual success"), "text/plain"); err != nil {
		t.Fatalf("Put after transient failure: %v", err)
	}
	if got := fake.count(http.MethodPut, key); got != 2 {
		t.Fatalf("PUT request count = %d, want 2", got)
	}
	fake.assertClean(t)
}

func TestClientEndpointSelection(t *testing.T) {
	for _, tc := range []struct {
		name           string
		preferInternal bool
		wantInternal   bool
	}{
		{name: "public by default", preferInternal: false, wantInternal: false},
		{name: "prefer internal", preferInternal: true, wantInternal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publicFake := newFakeOSS()
			publicServer := httptest.NewServer(publicFake)
			defer publicServer.Close()
			internalFake := newFakeOSS()
			internalServer := httptest.NewServer(internalFake)
			defer internalServer.Close()

			client := newTestClient(t, publicServer.URL, func(cfg *Config) {
				cfg.InternalEndpoint = internalServer.URL
				cfg.PreferInternal = tc.preferInternal
			})
			key := "endpoint-selection"
			if _, err := client.Put(context.Background(), key, []byte("selected"), "text/plain"); err != nil {
				t.Fatalf("Put: %v", err)
			}

			publicCount := publicFake.count(http.MethodPut, key)
			internalCount := internalFake.count(http.MethodPut, key)
			if tc.wantInternal {
				if publicCount != 0 || internalCount != 1 {
					t.Fatalf("public/internal request counts = %d/%d, want 0/1", publicCount, internalCount)
				}
			} else if publicCount != 1 || internalCount != 0 {
				t.Fatalf("public/internal request counts = %d/%d, want 1/0", publicCount, internalCount)
			}
			publicFake.assertClean(t)
			internalFake.assertClean(t)
		})
	}
}

func TestAccessKeySecretNeverSerializedIntoRequest(t *testing.T) {
	fake := newFakeOSS()
	server := httptest.NewServer(fake)
	defer server.Close()
	client := newTestClient(t, server.URL, nil)

	key := "secret-boundary"
	if _, err := client.Put(context.Background(), key, []byte("ordinary payload"), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := client.Get(context.Background(), key); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := client.Head(context.Background(), key); err != nil {
		t.Fatalf("Head: %v", err)
	}
	if err := client.Delete(context.Background(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	fake.assertClean(t)
}

func TestNewClientValidation(t *testing.T) {
	valid := Config{
		Endpoint:        "https://oss.example.test",
		Bucket:          testBucket,
		AccessKeyID:     testAK,
		AccessKeySecret: testSecret,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "missing endpoint", mutate: func(c *Config) { c.Endpoint = "" }},
		{name: "missing bucket", mutate: func(c *Config) { c.Bucket = "" }},
		{name: "missing access key id", mutate: func(c *Config) { c.AccessKeyID = "" }},
		{name: "missing secret", mutate: func(c *Config) { c.AccessKeySecret = "" }},
		{name: "bad scheme", mutate: func(c *Config) { c.Endpoint = "ftp://oss.example.test" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)
			if _, err := NewClient(cfg); err == nil {
				t.Fatal("NewClient unexpectedly succeeded")
			} else if strings.Contains(err.Error(), testSecret) {
				t.Fatalf("NewClient error leaked secret: %v", err)
			}
		})
	}
}

func ExampleNewClient() {
	_, _ = NewClient(Config{
		Endpoint:        "https://oss-cn-hangzhou.aliyuncs.com",
		Bucket:          "private-bucket",
		AccessKeyID:     "runtime-access-key-id",
		AccessKeySecret: "loaded-from-a-0600-file",
	})
	fmt.Println("configured")
	// Output: configured
}
