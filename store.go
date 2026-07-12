package main

import (
	"strings"
	"sync"
	"time"
)

const (
	cleanupEvery = 30 * time.Minute
	contextTTL   = 1 * time.Hour
	rpmWindow    = 1 * time.Minute
)

type memEntry struct {
	userLine string
	botResp  string
}

type chatMemory struct {
	entries  []memEntry
	lastUsed time.Time
}

type store struct {
	memMu    sync.Mutex
	contexts map[int64]*chatMemory

	rpmMu   sync.Mutex
	userRPM map[int64][]time.Time

	maxMessages int
	maxRunes    int
	rpmLimit    int
}

func newStore(rpmLimit, maxMessages, maxRunes int) *store {
	s := &store{
		contexts:    make(map[int64]*chatMemory),
		userRPM:     make(map[int64][]time.Time),
		rpmLimit:    rpmLimit,
		maxMessages: maxMessages,
		maxRunes:    maxRunes,
	}
	go s.cleanupLoop()
	return s
}

// checkRPM بررسی میکند کاربر میتواند درخواست بفرستد
func (s *store) checkRPM(userID int64) bool {
	now := time.Now()

	s.rpmMu.Lock()
	defer s.rpmMu.Unlock()

	times := s.userRPM[userID]

	valid := times[:0]
	for _, t := range times {
		if now.Sub(t) < rpmWindow {
			valid = append(valid, t)
		}
	}

	if len(valid) >= s.rpmLimit {
		s.userRPM[userID] = valid
		return false
	}

	s.userRPM[userID] = append(valid, now)
	return true
}

// getContext تاریخچه گفتگو را برمیگرداند
func (s *store) getContext(chatID int64) string {
	s.memMu.Lock()
	defer s.memMu.Unlock()

	mem, ok := s.contexts[chatID]
	if !ok || len(mem.entries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Previous conversation:\n")
	for _, e := range mem.entries {
		sb.WriteString(e.userLine)
		sb.WriteByte('\n')
		sb.WriteString("Bot: ")
		sb.WriteString(e.botResp)
		sb.WriteByte('\n')
	}
	sb.WriteByte('\n')
	return sb.String()
}

// saveMessage پیام جدید را ذخیره میکند
func (s *store) saveMessage(chatID int64, userLine, botResp string) {
	s.memMu.Lock()
	defer s.memMu.Unlock()

	mem := s.contexts[chatID]
	if mem == nil {
		mem = &chatMemory{
			entries: make([]memEntry, 0, s.maxMessages),
		}
		s.contexts[chatID] = mem
	}
	mem.lastUsed = time.Now()

	mem.entries = append(mem.entries, memEntry{
		userLine: truncate(userLine, s.maxRunes),
		botResp:  truncate(botResp, s.maxRunes),
	})

	if len(mem.entries) > s.maxMessages {
		copy(mem.entries, mem.entries[len(mem.entries)-s.maxMessages:])
		mem.entries = mem.entries[:s.maxMessages]
	}
}

func (s *store) cleanupLoop() {
	ticker := time.NewTicker(cleanupEvery)
	defer ticker.Stop()
	for range ticker.C {
		s.cleanup()
	}
}

func (s *store) cleanup() {
	now := time.Now()

	s.memMu.Lock()
	for id, mem := range s.contexts {
		if now.Sub(mem.lastUsed) > contextTTL {
			delete(s.contexts, id)
		}
	}
	s.memMu.Unlock()

	s.rpmMu.Lock()
	for id, times := range s.userRPM {
		valid := times[:0]
		for _, t := range times {
			if now.Sub(t) < rpmWindow {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(s.userRPM, id)
		} else {
			s.userRPM[id] = valid
		}
	}
	s.rpmMu.Unlock()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}