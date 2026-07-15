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
	"sync"
	"time"
)

// ── Config Types ──────────────────────────────────

type geminiErrorArray []struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

type modelConfig struct {
	Name string `yaml:"name"`
	RPM  int    `yaml:"rpm"`
	RPD  int    `yaml:"rpd"`
}

type providerConfig struct {
	Endpoint string        `yaml:"endpoint"`
	KeyEnv   string        `yaml:"key_env"`
	Models   []modelConfig `yaml:"models"`
	Free     bool          `yaml:"free"`
}

// ── API Types ─────────────────────────────────────

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

// ── Rate Tracker ──────────────────────────────────

type rateTracker struct {
	mu          sync.Mutex
	rpmLimit    int
	rpmRequests []time.Time
	rpdLimit    int
	rpdRequests []time.Time
}

func newRateTracker(rpm, rpd int) *rateTracker {
	return &rateTracker{
		rpmLimit: rpm,
		rpdLimit: rpd,
	}
}

func (r *rateTracker) allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.rpmLimit == 0 || r.rpdLimit == 0 {
		return false
	}

	now := time.Now()

	// filter RPM window (1 minute)
	rpmCut := now.Add(-time.Minute)
	fresh := r.rpmRequests[:0]
	for _, t := range r.rpmRequests {
		if t.After(rpmCut) {
			fresh = append(fresh, t)
		}
	}
	r.rpmRequests = fresh

	// filter RPD window (24 hours)
	rpdCut := now.Add(-24 * time.Hour)
	freshD := r.rpdRequests[:0]
	for _, t := range r.rpdRequests {
		if t.After(rpdCut) {
			freshD = append(freshD, t)
		}
	}
	r.rpdRequests = freshD

	if len(r.rpmRequests) >= r.rpmLimit {
		return false
	}
	if len(r.rpdRequests) >= r.rpdLimit {
		return false
	}

	r.rpmRequests = append(r.rpmRequests, now)
	r.rpdRequests = append(r.rpdRequests, now)
	return true
}

func (r *rateTracker) remaining() (rpm, rpd int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	usedRPM := 0
	cut := now.Add(-time.Minute)
	for _, t := range r.rpmRequests {
		if t.After(cut) {
			usedRPM++
		}
	}

	usedRPD := 0
	cutD := now.Add(-24 * time.Hour)
	for _, t := range r.rpdRequests {
		if t.After(cutD) {
			usedRPD++
		}
	}

	return r.rpmLimit - usedRPM, r.rpdLimit - usedRPD
}

// ── Model Entry ───────────────────────────────────

type modelEntry struct {
	endpoint string
	key      string
	name     string
	tracker  *rateTracker
	client   *http.Client
}

func (e *modelEntry) generate(ctx context.Context, system, prompt string) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model: e.name,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: prompt},
		},
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

	trimmed := bytes.TrimSpace(respBody)

	// Google sometimes returns errors as a JSON array
	// instead of an object, e.g. quota exhausted errors.
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var errArr geminiErrorArray
		if jerr := json.Unmarshal(trimmed, &errArr); jerr == nil && len(errArr) > 0 {
			e := errArr[0].Error
			return "", fmt.Errorf("api error [%d/%s]: %s", e.Code, e.Status, e.Message)
		}
		return "", fmt.Errorf("unexpected array response")
	}

	var cr chatResponse
	if err := json.Unmarshal(trimmed, &cr); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}

	if cr.Error != nil {
		return "", fmt.Errorf("api error [%v]: %s", cr.Error.Code, cr.Error.Message)
	}

	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("empty response from %s", e.name)
	}

	text := strings.TrimSpace(cr.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("empty content from %s", e.name)
	}

	return text, nil
}

// ── HTTP Client ───────────────────────────────────

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

// ── Router ────────────────────────────────────────

type providerRouter struct {
	models []*modelEntry
	system string
}

func newProviderRouter(cfgs []providerConfig, keys map[string]string, system string) (*providerRouter, error) {
	if len(cfgs) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	client := newHTTPClient()
	models := make([]*modelEntry, 0)

	for _, cfg := range cfgs {
		key := keys[cfg.KeyEnv]
		if key == "" {
			slog.Warn("provider key not set, skipping",
				"key_env", cfg.KeyEnv,
			)
			continue
		}

		for _, m := range cfg.Models {
			if m.RPM == 0 || m.RPD == 0 {
				slog.Info("model disabled, skipping",
					"model", m.Name,
				)
				continue
			}
			models = append(models, &modelEntry{
				endpoint: cfg.Endpoint,
				key:      key,
				name:     m.Name,
				tracker:  newRateTracker(m.RPM, m.RPD),
				client:   client,
			})
			slog.Info("model loaded",
				"model", m.Name,
				"rpm", m.RPM,
				"rpd", m.RPD,
			)
		}
	}

	if len(models) == 0 {
		return nil, fmt.Errorf("no models available")
	}

	return &providerRouter{models: models, system: system}, nil
}

func (r *providerRouter) generate(ctx context.Context, prompt string) (string, error) {
	var lastErr error
	attempted := 0

	for _, m := range r.models {
		if !m.tracker.allow() {
			rpmLeft, rpdLeft := m.tracker.remaining()
			slog.Info("model rate limited, skipping",
				"model", m.name,
				"rpm_left", rpmLeft,
				"rpd_left", rpdLeft,
			)
			continue
		}

		attempted++
		slog.Info("trying model", "model", m.name, "attempt", attempted)

		text, err := m.generate(ctx, r.system, prompt)
		if err == nil {
			if attempted > 1 {
				slog.Info("fallback succeeded", "model", m.name)
			}
			return text, nil
		}

		lastErr = err
		slog.Warn("model failed", "model", m.name, "err", err)

		// quota/rate errors -> skip immediately, no backoff
		if isQuotaError(err.Error()) {
			slog.Info("quota error, next model", "model", m.name)
			continue
		}

		// malformed response -> skip immediately, no backoff
		if isMalformedError(err.Error()) {
			slog.Info("malformed response, next model", "model", m.name)
			continue
		}

		// unknown non-retryable error -> stop completely
		if !isRetryable(err.Error()) {
			slog.Error("non-retryable error", "model", m.name)
			break
		}

		// only real transient errors get a backoff wait
		wait := backoff(attempted)
		slog.Info("backoff", "wait", wait)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}

	if attempted == 0 {
		return "", fmt.Errorf("all models are rate limited")
	}
	return "", fmt.Errorf("all models failed: %w", lastErr)
}

func isQuotaError(errStr string) bool {
	lower := strings.ToLower(errStr)
	for _, p := range []string{
		"quota", "rate", "limit",
		"exhausted", "exceeded",
		"resource", "429",
	} {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// isMalformedError: bad/unexpected response shape
// skip fast, retrying won't help
func isMalformedError(errStr string) bool {
	lower := strings.ToLower(errStr)
	for _, p := range []string{
		"unmarshal", "array", "empty response",
		"empty content",
	} {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// isRetryable: real transient errors worth waiting for
func isRetryable(errStr string) bool {
	lower := strings.ToLower(errStr)
	for _, p := range []string{
		"timeout", "deadline", "overloaded",
		"503", "502", "500",
	} {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func backoff(attempt int) time.Duration {
	base := time.Second
	max := 15 * time.Second
	d := base * (1 << min(attempt, 4))
	if d > max {
		d = max
	}
	jitter := time.Duration(rand.Int64N(int64(d / 2)))
	return d + jitter
}
