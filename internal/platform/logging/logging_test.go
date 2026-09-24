package logging_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"video-processor/internal/platform/logging"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"debug", slog.LevelDebug, false},
		{"INFO", slog.LevelInfo, false},
		{"", slog.LevelInfo, false},
		{" warn ", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"verbose", 0, true},
	}
	for _, tt := range tests {
		got, err := logging.ParseLevel(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseLevel(%q) = %v, %v", tt.in, got, err)
		}
	}
}

func TestNewWritesJSONWithService(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(&buf, "api", slog.LevelInfo)
	log.Debug("hidden")
	log.Info("hello", "k", 1)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (debug filtered): %q", len(lines), buf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if rec["msg"] != "hello" || rec["service"] != "api" || rec["level"] != "INFO" || rec["k"] != float64(1) {
		t.Errorf("unexpected record %v", rec)
	}
}
