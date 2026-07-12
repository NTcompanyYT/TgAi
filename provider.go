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
	Endpoint string `yaml:"endpoint"`
	KeyEnv   string `yaml:"key_env"`
	Model    string `yaml:"model"`
	Free     bool   `yaml:"free"`
}

type provider struct {
	cfg    providerConfig
	key    string
	client *http.Client
}

// OpenAI-compatible request/response
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

// ─── Provider ─────────────────────────────────────────────────────────────────

func newProvider(cfg providerConfig, key string) *provider {
	return &provider{
		cfg:    cfg,
		key:    key,
		client: newHTTPClient(),
	}
}

// generate یک پاسخ از این provider میگیرد
func (p *provider) generate(ctx context.Context, system, prompt string) (string, error) {
	messages := []chatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: prompt},
	}

	reqBody := chatRequest{
		Model:    p.cfg.Model,
		Messages: messages,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	url := strings.TrimRight(p.cfg.Endpoint, "/") + "/v1/chat/completions"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("User-Agent", "TelegramBot/1.0")

	resp, err := p.client.Do(req)
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

	// بررسی خطا از API
	if chatResp.Error != nil {
		return "", fmt.Errorf("api error: %s", chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("empty response from %s/%s", p.cfg.Endpoint, p.cfg.Model)
	}

	text := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("empty content from %s/%s", p.cfg.Endpoint, p.cfg.Model)
	}

	return text, nil
}

// ─── Router ───────────────────────────────────────────────────────────────────

type providerRouter struct {
	providers []*provider
	system    string
}

func newProviderRouter(cfgs []providerConfig, keys map[string]string, system string) (*providerRouter, error) {
	if len(cfgs) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	providers := make([]*provider, 0, len(cfgs))
	for _, cfg := range cfgs {
		key := keys[cfg.KeyEnv]
		if key == "" {
			slog.Warn("provider key not set, skipping",
				"model", cfg.Model,
				"key_env", cfg.KeyEnv,
			)
			continue
		}
		providers = append(providers, newProvider(cfg, key))
		slog.Info("provider loaded",
			"model", cfg.Model,
			"free", cfg.Free,
		)
	}

	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers with valid keys found")
	}

	return &providerRouter{
		providers: providers,
		system:    system,
	}, nil
}

// generate با fallback و exponential backoff
func (r *providerRouter) generate(ctx context.Context, prompt string) (string, error) {
	var lastErr error

	for i, p := range r.providers {
		slog.Info("trying provider",
			"model", p.cfg.Model,
			"attempt", i+1,
			"total", len(r.providers),
		)

		text, err := p.generate(ctx, r.system, prompt)
		if err == nil {
			if i > 0 {
				slog.Info("fallback succeeded",
					"model", p.cfg.Model,
					"after_attempts", i,
				)
			}
			return text, nil
		}

		lastErr = err
		slog.Warn("provider failed",
			"model", p.cfg.Model,
			"err", err,
		)

		// اگر خطا قابل retry نیست، بقیه را امتحان نکن
		if !isRetryable(err.Error()) {
			slog.Error("non-retryable error",
				"model", p.cfg.Model,
				"err", err,
			)
			break
		}

		// صبر با exponential backoff قبل از provider بعدی
		if i < len(r.providers)-1 {
			wait := backoff(i)
			slog.Info("waiting before next provider", "wait", wait)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(wait):
			}
		}
	}

	return "", fmt.Errorf("all providers failed, last error: %w", lastErr)
}

// isRetryable بررسی میکند خطا قابل retry است
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

// backoff زمان انتظار exponential با jitter
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