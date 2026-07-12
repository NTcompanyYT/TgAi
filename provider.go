package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"
)

// ─── Types ────────────────────────────────────────────────────────────────────

type providerConfig struct {
	Endpoint string   `yaml:"endpoint"`
	KeyEnv   string   `yaml:"key_env"`
	Models   []string `yaml:"models"`
	Free     bool     `yaml:"free"`
}

// providerEntry یک endpoint + یک مدل
type providerEntry struct {
	endpoint string
	key      string
	model    string
	client   *http.Client
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// ─── HTTP Client ──────────────────────────────────────────────────────────────

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// ─── Entry Generate ───────────────────────────────────────────────────────────

func (e *providerEntry) generate(ctx context.Context, system, prompt string) (string, error) {
	messages := []chatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: prompt},
	}

	body, err := json.Marshal(chatRequest{
		Model:    e.model,
		Messages: messages,
	})
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	url := strings.TrimRight(e.endpoint, "/") + "/v1/chat/completions"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.key)
	req.Header.Set("User-Agent", "TelegramBot/1.0")

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}

	if chatResp.Error != nil {
		return "", fmt.Errorf("api error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("empty response: %s/%s", e.endpoint, e.model)
	}

	text := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("empty content: %s/%s", e.endpoint, e.model)
	}

	return text, nil
}

// ─── Router ───────────────────────────────────────────────────────────────────

type providerRouter struct {
	entries []*providerEntry
	system  string
}

func newProviderRouter(cfgs []providerConfig, keys map[string]string, system string) (*providerRouter, error) {
	if len(cfgs) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	client := newHTTPClient()
	entries := make([]*providerEntry, 0)

	for _, cfg := range cfgs {
		key := keys[cfg.KeyEnv]
		if key == "" {
			slog.Warn("provider key not set, skipping",
				"key_env", cfg.KeyEnv,
			)
			continue
		}

		for _, model := range cfg.Models {
			entries = append(entries, &providerEntry{
				endpoint: cfg.Endpoint,
				key:      key,
				model:    model,
				client:   client,
			})
			slog.Info("provider loaded",
				"model", model,
				"free", cfg.Free,
			)
		}
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("no providers with valid keys found")
	}

	return &providerRouter{
		entries: entries,
		system:  system,
	}, nil
}

// generate با fallback و exponential backoff
func (r *providerRouter) generate(ctx context.Context, prompt string) (string, error) {
	var lastErr error

	for i, e := range r.entries {
		slog.Info("trying provider",
			"model", e.model,
			"attempt", i+1,
			"total", len(r.entries),
		)

		text, err := e.generate(ctx, r.system, prompt)
		if err == nil {
			if i > 0 {
				slog.Info("fallback succeeded",
					"model", e.model,
					"after_attempts", i,
				)
			}
			return text, nil
		}

		lastErr = err
		slog.Warn("provider failed",
			"model", e.model,
			"err", err,
		)

		if !isRetryable(err.Error()) {
			slog.Error("non-retryable error, stopping",
				"model", e.model,
				"err", err,
			)
			break
		}

		if i < len(r.entries)-1 {
			wait := backoff(i)
			slog.Info("waiting before next provider", "wait", wait)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(wait):
			}
		}
	}

	return "", fmt.Errorf("all providers failed: %w", lastErr)
}

func isRetryable(errStr string) bool {
	lower := strings.ToLower(errStr)
	for _, p := range []string{
		"rate", "quota", "limit", "overloaded",
		"429", "503", "502", "500",
		"timeout", "deadline",
		"not found", "404",
	} {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func backoff(attempt int) time.Duration {
	base := time.Second
	max := 30 * time.Second
	d := base * (1 << min(attempt, 5))
	if d > max {
		d = max
	}
	jitter := time.Duration(rand.Int64N(int64(d / 2)))
	return d + jitter
}