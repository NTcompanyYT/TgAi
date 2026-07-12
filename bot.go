package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	tgMaxBodySize  = 10 * 1024 * 1024 // 10 MB
	tgAPITimeout   = 30 * time.Second
	maxImageSize   = 15 * 1024 * 1024 // 15 MB
	maxWebhookBody = 1 * 1024 * 1024  // 1 MB
	tgMaxText      = 4096
)

// ─── Telegram Types ───────────────────────────────────────────────────────────

type update struct {
	UpdateID int      `json:"update_id"`
	Message  *message `json:"message,omitempty"`
}

type message struct {
	MessageID      int      `json:"message_id"`
	From           *user    `json:"from,omitempty"`
	Chat           chat     `json:"chat"`
	Text           string   `json:"text,omitempty"`
	Entities       []entity `json:"entities,omitempty"`
	ReplyToMessage *message `json:"reply_to_message,omitempty"`
}

type user struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Description string          `json:"description,omitempty"`
}

type getMeResult struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	Username string `json:"username"`
}

// ─── Telegram Client ──────────────────────────────────────────────────────────

type tgClient struct {
	token    string
	baseURL  string
	username string
	http     *http.Client
}

func newTGClient(token string) (*tgClient, error) {
	if token == "" {
		return nil, fmt.Errorf("telegram token is empty")
	}

	c := &tgClient{
		token:   token,
		baseURL: "https://api.telegram.org/bot" + token,
		http: &http.Client{
			Timeout: tgAPITimeout,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   10,
				IdleConnTimeout:       90 * time.Second,
				ForceAttemptHTTP2:     true,
			},
			CheckRedirect: func(r *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}

	me, err := c.getMe()
	if err != nil {
		return nil, fmt.Errorf("validate token: %w", err)
	}
	c.username = me.Username

	slog.Info("telegram ready", "username", c.username)
	return c, nil
}

func (c *tgClient) do(method string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), tgAPITimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TelegramBot/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, tgMaxBodySize))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var ar apiResponse
	if err := json.Unmarshal(respBody, &ar); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if !ar.OK {
		return nil, fmt.Errorf("api error %d: %s", ar.ErrorCode, ar.Description)
	}
	return ar.Result, nil
}

func (c *tgClient) getMe() (*getMeResult, error) {
	result, err := c.do("getMe", struct{}{})
	if err != nil {
		return nil, err
	}
	var me getMeResult
	return &me, json.Unmarshal(result, &me)
}

func (c *tgClient) setWebhook(webhookURL string) error {
	_, err := c.do("setWebhook", map[string]any{
		"url":             webhookURL,
		"allowed_updates": []string{"message"},
		"drop_pending_updates": true,
		"max_connections": 100,
	})
	if err != nil {
		return fmt.Errorf("setWebhook: %w", err)
	}
	slog.Info("webhook set", "url", webhookURL)
	return nil
}

func (c *tgClient) sendMessage(chatID int64, text string, replyToID int) error {
	if text == "" {
		return nil
	}
	// محدودیت تلگرام
	runes := []rune(text)
	if len(runes) > tgMaxText {
		text = string(runes[:tgMaxText])
	}

	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if replyToID > 0 {
		payload["reply_parameters"] = map[string]int{"message_id": replyToID}
	}
	_, err := c.do("sendMessage", payload)
	return err
}

func (c *tgClient) sendChatAction(chatID int64, action string) error {
	_, err := c.do("sendChatAction", map[string]any{
		"chat_id": chatID,
		"action":  action,
	})
	return err
}

func (c *tgClient) sendPhotoBytes(chatID int64, filename string, data []byte, caption string, replyToID int) error {
	if len(data) == 0 {
		return fmt.Errorf("empty photo data")
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		_ = w.WriteField("caption", caption)
	}
	if replyToID > 0 {
		rp, _ := json.Marshal(map[string]int{"message_id": replyToID})
		_ = w.WriteField("reply_parameters", string(rp))
	}
	part, err := w.CreateFormFile("photo", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	_ = w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/sendPhoto", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, tgMaxBodySize))
	var ar apiResponse
	if err := json.Unmarshal(respBody, &ar); err != nil {
		return err
	}
	if !ar.OK {
		return fmt.Errorf("api error %d: %s", ar.ErrorCode, ar.Description)
	}
	return nil
}

// ─── Worker Pool ──────────────────────────────────────────────────────────────

type pool struct {
	jobs    chan update
	handler func(update)
	wg      sync.WaitGroup
	once    sync.Once
}

func newPool(workers, queueSize int, handler func(update)) *pool {
	p := &pool{
		jobs:    make(chan update, queueSize),
		handler: handler,
	}
	for range workers {
		p.wg.Add(1)
		go p.worker()
	}
	slog.Info("pool started", "workers", workers, "queue", queueSize)
	return p
}

func (p *pool) submit(u update) {
	select {
	case p.jobs <- u:
	default:
		slog.Warn("queue full, dropping update", "update_id", u.UpdateID)
	}
}

func (p *pool) stop() {
	p.once.Do(func() {
		close(p.jobs)
		p.wg.Wait()
	})
}

func (p *pool) worker() {
	defer p.wg.Done()
	for u := range p.jobs {
		p.safe(u)
	}
}

func (p *pool) safe(u update) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			slog.Error("panic recovered",
				"panic", r,
				"stack", string(buf[:n]),
				"update_id", u.UpdateID,
			)
		}
	}()
	p.handler(u)
}

// ─── Bot ──────────────────────────────────────────────────────────────────────

type bot struct {
	tg      *tgClient
	router  *providerRouter
	store   *store
	adminID int64
}

func newBot(tg *tgClient, router *providerRouter, store *store, adminID int64) *bot {
	return &bot{tg: tg, router: router, store: store, adminID: adminID}
}

func (b *bot) handleUpdate(u update) {
	msg := u.Message
	if msg == nil || msg.From == nil {
		return
	}

	// ─── دستور /image ──────────────────────────────────────
	if cmd, args := parseCommand(msg); cmd == "image" {
		b.handleImage(msg, args)
		return
	}

	// ─── فقط متن ───────────────────────────────────────────
	if msg.Text == "" {
		return
	}

	// ─── reply یا mention ──────────────────────────────────
	mention := "@" + b.tg.username
	isReply := msg.ReplyToMessage != nil &&
		msg.ReplyToMessage.From != nil &&
		msg.ReplyToMessage.From.Username == b.tg.username
	isMention := strings.Contains(msg.Text, mention)

	if !isReply && !isMention {
		return
	}

	// ─── پاکسازی متن ───────────────────────────────────────
	txt := strings.ReplaceAll(msg.Text, mention, "")
	txt = strings.TrimSpace(txt)
	txt = sanitize(txt)
	if txt == "" {
		_ = b.tg.sendMessage(msg.Chat.ID, "⚠️ Empty or invalid text.", msg.MessageID)
		return
	}

	// ─── Rate Limit ─────────────────────────────────────────
	if !b.store.checkRPM(msg.From.ID) {
		_ = b.tg.sendMessage(msg.Chat.ID,
			"⏳ Rate limit reached. Please wait a moment.",
			msg.MessageID)
		return
	}

	firstName := sanitize(msg.From.FirstName)
	lastName := sanitize(msg.From.LastName)
	userName := strings.TrimSpace(firstName + " " + lastName)
	if userName == "" {
		userName = fmt.Sprintf("user_%d", msg.From.ID)
	}

	slog.Info("request",
		"user_id", msg.From.ID,
		"username", msg.From.Username,
		"chat_id", msg.Chat.ID,
		"text_len", len(txt),
	)

	// ─── Context ────────────────────────────────────────────
	history := b.store.getContext(msg.Chat.ID)
	prompt := history + userName + ": " + txt

	_ = b.tg.sendChatAction(msg.Chat.ID, "typing")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	answer, err := b.router.generate(ctx, prompt)
	if err != nil {
		slog.Error("generate failed", "err", err)
		b.sendError(msg.Chat.ID, msg.MessageID, err)
		return
	}

	b.store.saveMessage(msg.Chat.ID, userName+": "+txt, answer)

	if err := b.tg.sendMessage(msg.Chat.ID, answer, msg.MessageID); err != nil {
		slog.Error("send message failed", "err", err)
	}
}

func (b *bot) handleImage(msg *message, prompt string) {
	if prompt == "" {
		_ = b.tg.sendMessage(msg.Chat.ID,
			"❌ Please provide an image description.\nExample: /image a futuristic city",
			msg.MessageID)
		return
	}

	if !b.store.checkRPM(msg.From.ID) {
		_ = b.tg.sendMessage(msg.Chat.ID,
			"⏳ Rate limit reached. Please wait.",
			msg.MessageID)
		return
	}

	slog.Info("image request",
		"user_id", msg.From.ID,
		"prompt", prompt,
	)

	_ = b.tg.sendChatAction(msg.Chat.ID, "upload_photo")

	encoded := url.PathEscape(prompt)
	imageURL := fmt.Sprintf(
		"https://image.pollinations.ai/prompt/%s?width=1024&height=1024&nologo=true&seed=%d",
		encoded, time.Now().UnixNano(),
	)

	data, filename, err := fetchImage(imageURL)
	if err != nil {
		slog.Error("fetch image failed", "err", err)
		_ = b.tg.sendMessage(msg.Chat.ID,
			"⚠️ Failed to fetch image. Please try again.",
			msg.MessageID)
		return
	}

	caption := "🖼 " + prompt
	if err := b.tg.sendPhotoBytes(msg.Chat.ID, filename, data, caption, msg.MessageID); err != nil {
		slog.Error("send photo failed", "err", err)
		_ = b.tg.sendMessage(msg.Chat.ID,
			"⚠️ Failed to send image. Please try again.",
			msg.MessageID)
	}
}

func (b *bot) sendError(chatID int64, replyToID int, err error) {
	e := err.Error()
	msg := "⚠️ Request processing error\n\n"
	switch {
	case strings.Contains(e, "timeout") || strings.Contains(e, "deadline"):
		msg += "Response timeout. Please try again."
	case strings.Contains(e, "rate") || strings.Contains(e, "quota"):
		msg += "High server traffic. Please wait a moment."
	case strings.Contains(e, "safety") || strings.Contains(e, "blocked"):
		msg += "Content doesn't meet safety guidelines."
	default:
		msg += "Please try again."
	}
	_ = b.tg.sendMessage(chatID, msg, replyToID)
}

// webhookHandler درخواست‌های تلگرام را دریافت میکند
func webhookHandler(token string, submit func(update)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBody)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			slog.Warn("webhook read error", "err", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var u update
		if err := json.Unmarshal(body, &u); err != nil {
			slog.Warn("webhook unmarshal error", "err", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)
		submit(u)
	}
}

// ─── Image Fetch ──────────────────────────────────────────────────────────────

var imageClient = &http.Client{
	Timeout: 60 * time.Second,
	CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

func fetchImage(imageURL string) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := imageClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("image server: %s", resp.Status)
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "image/") {
		return nil, "", fmt.Errorf("unexpected content-type: %s", ct)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxImageSize {
		return nil, "", fmt.Errorf("image too large")
	}

	filename := "image.jpg"
	switch {
	case strings.Contains(ct, "png"):
		filename = "image.png"
	case strings.Contains(ct, "webp"):
		filename = "image.webp"
	case strings.Contains(ct, "gif"):
		filename = "image.gif"
	}

	return data, filename, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func sanitize(text string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if !utf8.ValidRune(r) || r == 0 {
			continue
		}
		if r != '\n' && r != '\t' && r < 32 {
			continue
		}
		if r >= 0x1D400 && r <= 0x1D7FF {
			continue
		}
		b.WriteRune(r)
	}
	result := b.String()
	if !utf8.ValidString(result) {
		return ""
	}
	return result
}

func parseCommand(msg *message) (cmd, args string) {
	if msg.Text == "" {
		return "", ""
	}
	for _, e := range msg.Entities {
		if e.Type != "bot_command" || e.Offset != 0 {
			continue
		}
		runes := []rune(msg.Text)
		if e.Length > len(runes) {
			continue
		}
		full := strings.TrimPrefix(string(runes[:e.Length]), "/")
		if idx := strings.Index(full, "@"); idx != -1 {
			full = full[:idx]
		}
		return full, strings.TrimSpace(string(runes[e.Length:]))
	}
	return "", ""
}