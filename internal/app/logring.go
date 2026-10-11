package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LogEntry is one line shown in the dashboard's log drawer.
type LogEntry struct {
	T     int64  `json:"t"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
	Attrs string `json:"attrs,omitempty"`
}

// Ring keeps the most recent log entries in memory.
type Ring struct {
	mu   sync.Mutex
	buf  []LogEntry
	next int
	full bool
}

func NewRing(n int) *Ring { return &Ring{buf: make([]LogEntry, n)} }

func (r *Ring) add(e LogEntry) {
	r.mu.Lock()
	r.buf[r.next] = e
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

func (r *Ring) Entries() []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []LogEntry
	if r.full {
		out = append(out, r.buf[r.next:]...)
	}
	return append(out, r.buf[:r.next]...)
}

// teeHandler writes to an inner handler and to the ring.
type teeHandler struct {
	inner slog.Handler
	ring  *Ring
	attrs []slog.Attr
}

func (h *teeHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	var sb strings.Builder
	write := func(a slog.Attr) bool {
		if sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%s=%v", a.Key, a.Value)
		return true
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(write)
	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	h.ring.add(LogEntry{T: t.UnixMilli(), Level: strings.ToLower(r.Level.String()), Msg: r.Message, Attrs: sb.String()})
	return h.inner.Handle(ctx, r)
}

func (h *teeHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &teeHandler{inner: h.inner.WithAttrs(as), ring: h.ring, attrs: append(append([]slog.Attr{}, h.attrs...), as...)}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{inner: h.inner.WithGroup(name), ring: h.ring, attrs: h.attrs}
}

func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
