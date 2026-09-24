// Package logging builds the structured (JSON, log/slog) logger every
// service uses.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// ParseLevel parses debug, info, warn or error (case-insensitive).
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("invalid log level %q: want debug, info, warn or error", s)
}

// New returns a JSON logger writing to w at the given level, with a
// "service" attribute on every record and the correlation fields of the
// record's context (ContextHandler) on records logged with a *Context
// method.
func New(w io.Writer, service string, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(NewContextHandler(h)).With(slog.String("service", service))
}
