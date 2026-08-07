package agent

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Client talks to the control plane with agent request signing.
type Client struct {
	baseURL    string
	http       *http.Client
	credential string
	timeout    time.Duration
}

// NewClient builds an agent HTTP client.
func NewClient(baseURL string, insecureSkipVerify bool, timeout time.Duration) *Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureSkipVerify},
	}
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Transport: tr, Timeout: timeout},
		timeout: timeout,
	}
}

// SetCredential updates the node credential used for signing.
func (c *Client) SetCredential(cred string) { c.credential = cred }

// Sign computes the agent request signature.
func Sign(credential, ts, method, path, bodyHash string) string {
	mac := hmac.New(sha256.New, []byte(credential))
	mac.Write([]byte(ts + "|" + method + "|" + path + "|" + bodyHash))
	return hex.EncodeToString(mac.Sum(nil))
}

// Do performs a signed request. body may be nil. respBody is decoded into out
// when non-nil.
func (c *Client) Do(method, path string, body any, out any) (*http.Response, error) {
	var bodyBytes []byte
	var err error
	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	sum := sha256.Sum256(bodyBytes)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := Sign(c.credential, ts, method, path, hex.EncodeToString(sum[:]))
	req, err := http.NewRequest(method, c.baseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	if c.credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.credential)
	}
	req.Header.Set("X-Agent-Timestamp", ts)
	req.Header.Set("X-Agent-Signature", sig)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if out != nil {
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp, err
		}
		if resp.StatusCode >= 300 {
			return resp, fmt.Errorf("control plane returned %d: %s", resp.StatusCode, truncate(string(data), 300))
		}
		if err := json.Unmarshal(data, out); err != nil {
			return resp, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp, nil
}

// UnsignedWithBearer performs an unsigned request with a Bearer token
// (used for the one-time enrollment claim).
func (c *Client) UnsignedWithBearer(method, path string, body any, token string, out any) (*http.Response, error) {
	var bodyBytes []byte
	var err error
	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest(method, c.baseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if out != nil {
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp, err
		}
		if resp.StatusCode >= 300 {
			return resp, fmt.Errorf("control plane returned %d: %s", resp.StatusCode, truncate(string(data), 300))
		}
		if err := json.Unmarshal(data, out); err != nil {
			return resp, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp, nil
}

// Unsigned performs a request without agent signing (enrollment flow).
func (c *Client) Unsigned(method, path string, body any, out any) (*http.Response, error) {
	var bodyBytes []byte
	var err error
	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest(method, c.baseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if out != nil {
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp, err
		}
		if resp.StatusCode >= 300 {
			return resp, fmt.Errorf("control plane returned %d: %s", resp.StatusCode, truncate(string(data), 300))
		}
		if err := json.Unmarshal(data, out); err != nil {
			return resp, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// SendEvent reports a task event (TaskReporter implementation).
func (c *Client) SendEvent(taskID string, ev Event) error {
	_, err := c.Do("POST", "/api/v1/agent/tasks/"+taskID+"/events", ev, nil)
	return err
}

// SendResult reports a task result (TaskReporter implementation).
func (c *Client) SendResult(taskID string, res Result) error {
	_, err := c.Do("POST", "/api/v1/agent/tasks/"+taskID+"/result", res, nil)
	return err
}
