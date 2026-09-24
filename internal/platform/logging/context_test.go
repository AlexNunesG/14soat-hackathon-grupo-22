package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"video-processor/internal/platform/logging"
)

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not JSON: %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestContextHandlerAddsContextFields(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(&buf, "worker", slog.LevelInfo)

	ctx := logging.WithRequestID(context.Background(), "req-1")
	ctx = logging.WithAttrs(ctx,
		slog.String(logging.KeyMessageID, "m-1"), slog.Int(logging.KeyAttempt, 2))
	ctx = logging.WithAttrs(ctx, slog.String(logging.KeyVideoID, "v-1"))
	log.InfoContext(ctx, "processing video", slog.String("k", "v"))
	log.With(slog.String("component", "x")).WarnContext(ctx, "derived logger")
	log.Info("no context") // without a context: no correlation fields

	recs := decodeLines(t, &buf)
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	for i, rec := range recs[:2] {
		for k, want := range map[string]any{
			"service": "worker", "request_id": "req-1", "message_id": "m-1",
			"attempt": float64(2), "video_id": "v-1",
		} {
			if rec[k] != want {
				t.Errorf("record %d: %s = %v, want %v (%v)", i, k, rec[k], want, rec)
			}
		}
	}
	if recs[0]["k"] != "v" || recs[1]["component"] != "x" {
		t.Errorf("record attributes lost: %v", recs[:2])
	}
	if _, ok := recs[2]["request_id"]; ok {
		t.Errorf("record without context has a request_id: %v", recs[2])
	}
}

func TestWithAttrsReplacesAndIgnoresEmpty(t *testing.T) {
	base := logging.WithRequestID(context.Background(), "a")
	if got := logging.RequestID(base); got != "a" {
		t.Fatalf("RequestID = %q", got)
	}
	replaced := logging.WithRequestID(base, "b")
	if logging.RequestID(replaced) != "b" || logging.RequestID(base) != "a" {
		t.Errorf("replace: got %q (parent %q)", logging.RequestID(replaced), logging.RequestID(base))
	}
	if same := logging.WithRequestID(base, ""); same != base {
		t.Error("an empty id changed the context")
	}
	if logging.RequestID(context.Background()) != "" {
		t.Error("RequestID of an empty context is not empty")
	}

	var buf bytes.Buffer
	log := logging.New(&buf, "api", slog.LevelInfo)
	log.InfoContext(replaced, "once")
	if n := strings.Count(buf.String(), `"request_id"`); n != 1 {
		t.Errorf("request_id appears %d times: %s", n, buf.String())
	}
}

func TestContextHandlerExplicitAttributesWin(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(&buf, "notifier", slog.LevelInfo)
	ctx := logging.WithAttrs(logging.WithRequestID(context.Background(), "r"),
		slog.String(logging.KeyVideoID, "from-ctx"), slog.String(logging.KeyEventID, "e-ctx"))

	log.With(slog.String(logging.KeyVideoID, "from-with")).InfoContext(ctx, "a")
	log.InfoContext(ctx, "b", slog.String(logging.KeyEventID, "from-record"))

	out := buf.String()
	for _, key := range []string{`"video_id"`, `"event_id"`, `"request_id"`} {
		if n := strings.Count(out, key); n != 2 {
			t.Errorf("%s appears %d times, want once per line: %s", key, n, out)
		}
	}
	recs := decodeLines(t, &buf)
	if recs[0]["video_id"] != "from-with" || recs[1]["event_id"] != "from-record" || recs[1]["video_id"] != "from-ctx" {
		t.Errorf("explicit attributes did not win: %v", recs)
	}
}

func TestValidRequestID(t *testing.T) {
	for id, want := range map[string]bool{
		"demo-123":               true,
		"a.b_c:d-E9":             true,
		strings.Repeat("a", 128): true,
		strings.Repeat("a", 129): false,
		"":                       false,
		"has space":              false,
		"new\nline":              false,
		`quote"`:                 false,
		"ümlaut":                 false,
	} {
		if got := logging.ValidRequestID(id); got != want {
			t.Errorf("ValidRequestID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{
		"ana@example.com":  "a***@example.com",
		"José@example.com": "J***@example.com",
		"no-at-sign":       "***",
		"@example.com":     "***",
	} {
		if got := logging.MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
