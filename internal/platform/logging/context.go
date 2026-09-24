package logging

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"
)

// Correlation fields (docs/observability.md). Every record logged with a
// *Context method (InfoContext, ErrorContext, LogAttrs...) carries the
// fields stored in its context by WithRequestID and WithAttrs.
const (
	// KeyRequestID is the correlation id: the X-Request-ID of the upload,
	// carried by every message it causes, so one grep follows a video
	// through the api, the worker and the notifier.
	KeyRequestID = "request_id"
	KeyUserID    = "user_id"
	KeyVideoID   = "video_id"
	KeyEventID   = "event_id"
	KeyMessageID = "message_id"
	KeyAttempt   = "attempt"
)

// MaxRequestIDLen bounds a request id accepted from outside (the
// X-Request-ID header, a message's correlation id).
const MaxRequestIDLen = 128

// ValidRequestID reports whether a request id received from outside can be
// used as is: 1 to MaxRequestIDLen characters among ASCII letters, digits
// and - _ . : so it is safe in logs, headers and message properties.
func ValidRequestID(id string) bool {
	if id == "" || len(id) > MaxRequestIDLen {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}

type ctxKey struct{}

// fields is the immutable list of attributes stored in a context.
type fields struct {
	attrs []slog.Attr
}

func fromContext(ctx context.Context) []slog.Attr {
	if ctx == nil {
		return nil
	}
	if f, ok := ctx.Value(ctxKey{}).(*fields); ok {
		return f.attrs
	}
	return nil
}

// WithAttrs returns a copy of ctx whose log records carry attrs. An
// attribute replaces one with the same key already in ctx; attributes with
// an empty key or an empty string value are ignored.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	cur := fromContext(ctx)
	next := make([]slog.Attr, len(cur), len(cur)+len(attrs))
	copy(next, cur)
	changed := false
	for _, a := range attrs {
		if a.Key == "" || (a.Value.Kind() == slog.KindString && a.Value.String() == "") {
			continue
		}
		changed = true
		replaced := false
		for i := range next {
			if next[i].Key == a.Key {
				next[i], replaced = a, true
				break
			}
		}
		if !replaced {
			next = append(next, a)
		}
	}
	if !changed {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, &fields{attrs: next})
}

// WithRequestID returns a copy of ctx carrying the correlation id id (see
// KeyRequestID). An empty id leaves ctx unchanged.
func WithRequestID(ctx context.Context, id string) context.Context {
	return WithAttrs(ctx, slog.String(KeyRequestID, id))
}

// RequestID returns the correlation id stored in ctx, or "".
func RequestID(ctx context.Context) string {
	for _, a := range fromContext(ctx) {
		if a.Key == KeyRequestID {
			return a.Value.String()
		}
	}
	return ""
}

// ContextHandler is a slog.Handler that adds the attributes stored in the
// record's context (WithAttrs, WithRequestID) to every record, then hands
// it to the wrapped handler. An attribute the record or the logger
// (Logger.With) already has wins over the context's, so no key appears
// twice in a line.
type ContextHandler struct {
	next slog.Handler
	// bound holds the keys of the attributes added by Logger.With outside
	// any group.
	bound map[string]bool
	// grouped is set once WithGroup was called: bound keys are then in a
	// group and cannot clash with the context's top-level attributes.
	grouped bool
}

// NewContextHandler wraps next.
func NewContextHandler(next slog.Handler) *ContextHandler { return &ContextHandler{next: next} }

// Enabled implements slog.Handler.
func (h *ContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := fromContext(ctx)
	if len(attrs) == 0 {
		return h.next.Handle(ctx, r)
	}
	present := make(map[string]bool, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		present[a.Key] = true
		return true
	})
	r = r.Clone()
	for _, a := range attrs {
		if !present[a.Key] && !h.bound[a.Key] {
			r.AddAttrs(a)
		}
	}
	return h.next.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	bound := h.bound
	if !h.grouped {
		bound = make(map[string]bool, len(h.bound)+len(attrs))
		for k := range h.bound {
			bound[k] = true
		}
		for _, a := range attrs {
			bound[a.Key] = true
		}
	}
	return &ContextHandler{next: h.next.WithAttrs(attrs), bound: bound, grouped: h.grouped}
}

// WithGroup implements slog.Handler.
func (h *ContextHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &ContextHandler{next: h.next.WithGroup(name), bound: h.bound, grouped: true}
}

// MaskEmail hides most of an e-mail address for logs: the first character
// of the local part and the domain are kept (ana@example.com becomes
// a***@example.com). Anything without an @ becomes "***". Log lines never
// carry full addresses: users are identified by their id.
func MaskEmail(addr string) string {
	local, domain, ok := strings.Cut(strings.TrimSpace(addr), "@")
	if !ok || local == "" {
		return "***"
	}
	r, _ := utf8.DecodeRuneInString(local)
	return string(r) + "***@" + domain
}
