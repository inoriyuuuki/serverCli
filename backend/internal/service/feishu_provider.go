package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// feishuTimeout is the fixed end-to-end timeout for Feishu webhook calls.
// Constructing a provider with a non-positive timeout falls back to it.
const feishuTimeout = 5 * time.Second

// maxFeishuResponseBytes bounds how much of a Feishu response we are willing
// to read for the business code check.
const maxFeishuResponseBytes = 1 << 20 // 1 MiB

// FeishuProvider sends text notifications to a Feishu group bot webhook. It
// never retries and never logs the webhook URL, request body or response body.
type FeishuProvider struct {
	webhookURL string
	timeout    time.Duration
	client     *http.Client
}

// NewFeishuProvider builds a Feishu provider. A non-positive timeout defaults
// to feishuTimeout; a nil client defaults to http.DefaultClient.
func NewFeishuProvider(webhookURL string, timeout time.Duration, client *http.Client) *FeishuProvider {
	if timeout <= 0 {
		timeout = feishuTimeout
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &FeishuProvider{webhookURL: webhookURL, timeout: timeout, client: client}
}

// Name returns the stable provider identifier.
func (p *FeishuProvider) Name() string { return "feishu" }

// Configured reports whether the provider has a webhook URL to send to.
func (p *FeishuProvider) Configured() bool { return p.webhookURL != "" }

// feishuResponse is the minimal business envelope returned by Feishu.
type feishuResponse struct {
	Code int `json:"code"`
}

// Send posts a plain-text message to the configured webhook. It checks both
// the HTTP status and the Feishu business code; only HTTP 2xx plus code==0 is
// considered success. No logging happens here and error values never include
// the webhook URL, request body or response body.
func (p *FeishuProvider) Send(ctx context.Context, req NotificationRequest) error {
	if p.webhookURL == "" {
		return ErrNotConfigured
	}
	payload := map[string]any{
		"msg_type": "text",
		"content": map[string]string{
			"text": req.Title + "\n" + req.Message,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: marshal payload", ErrUpstream)
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, p.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: build request", ErrUpstream)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		// The underlying error may embed the URL (e.g. from http.Client), so
		// never propagate it verbatim.
		if errors.Is(err, context.DeadlineExceeded) || callCtx.Err() != nil {
			return fmt.Errorf("%w: upstream timeout", ErrUpstream)
		}
		return fmt.Errorf("%w: upstream request failed", ErrUpstream)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%w: http status %d", ErrUpstream, resp.StatusCode)
	}

	var biz feishuResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxFeishuResponseBytes))
	if err := dec.Decode(&biz); err != nil {
		return fmt.Errorf("%w: invalid upstream response", ErrUpstream)
	}
	if biz.Code != 0 {
		return fmt.Errorf("%w: feishu business code %d", ErrUpstream, biz.Code)
	}
	return nil
}
