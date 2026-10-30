package logs

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// redactedValue replaces the value of a secret attribute in the buffer. The output
// handler is untouched: it writes what the code logged.
const redactedValue = "[redacted]"

// secretKeys are attribute keys (case-insensitive, exact) whose values are never kept.
var secretKeys = map[string]bool{
	"password":      true,
	"secret":        true,
	"token":         true,
	"api_key":       true,
	"apikey":        true,
	"authorization": true,
	"cookie":        true,
	"key":           true,
}

// Handler is a slog.Handler that passes every record to the inner handler (the
// normal output) and keeps a copy in the buffer. The buffer only ever sees the
// attributes the code logged: request and response bodies are never logged.
type Handler struct {
	inner  slog.Handler
	buf    *Buffer
	groups []string // open groups of WithGroup, outermost first
	attrs  []entry  // attributes of WithAttrs, each with the groups open at that point
}

type entry struct {
	groups []string
	attr   slog.Attr
}

// NewHandler wraps inner so that every record it handles is also recorded in buf.
func NewHandler(inner slog.Handler, buf *Buffer) *Handler {
	return &Handler{inner: inner, buf: buf}
}

// Enabled follows the inner handler: the buffer keeps what the output writes.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle records the record (before the inner handler may consume it) and writes it.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs()+len(h.attrs))
	for _, e := range h.attrs {
		put(attrs, e.groups, e.attr)
	}
	r.Attrs(func(a slog.Attr) bool {
		put(attrs, h.groups, a)
		return true
	})
	h.buf.add(Record{Time: r.Time, Level: r.Level, Msg: r.Message, Attrs: attrs})
	return h.inner.Handle(ctx, r)
}

// WithAttrs returns a handler whose records also carry as.
func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	if len(as) == 0 {
		return h
	}
	n := &Handler{inner: h.inner.WithAttrs(as), buf: h.buf, groups: h.groups, attrs: slices.Clip(h.attrs)}
	for _, a := range as {
		n.attrs = append(n.attrs, entry{groups: h.groups, attr: a})
	}
	return n
}

// WithGroup returns a handler whose attributes nest under name (an empty name is a no-op, as in slog).
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &Handler{inner: h.inner.WithGroup(name), buf: h.buf, groups: append(slices.Clip(h.groups), name), attrs: h.attrs}
}

// put stores one attribute under path (its open groups) in dst. Groups become nested
// maps; empty attributes are skipped, as slog does.
func put(dst map[string]any, path []string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		sub := path
		if a.Key != "" {
			sub = append(slices.Clip(path), a.Key)
		}
		for _, g := range a.Value.Group() {
			put(dst, sub, g)
		}
		return
	}
	m := dst
	for _, g := range path {
		next, ok := m[g].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[g] = next
		}
		m = next
	}
	m[a.Key] = value(path, a)
}

// value is the buffered form of an attribute value: redacted when the key or any
// enclosing group is a secret key, otherwise readable in JSON (errors as text,
// durations as text, the rest as the logged Go value).
func value(path []string, a slog.Attr) any {
	if isSecret(a.Key) || anySecret(path) {
		return redactedValue
	}
	switch v := a.Value.Any().(type) {
	case error:
		return v.Error()
	case time.Duration:
		return v.String()
	default:
		return v
	}
}

func isSecret(key string) bool {
	return secretKeys[strings.ToLower(key)]
}

func anySecret(path []string) bool {
	for _, g := range path {
		if isSecret(g) {
			return true
		}
	}
	return false
}
