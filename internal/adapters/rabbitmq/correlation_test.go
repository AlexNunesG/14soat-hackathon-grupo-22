package rabbitmq

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/app"
	"video-processor/internal/platform/logging"
)

const (
	testVideoID = "22222222-2222-4222-8222-222222222222"
	testEventID = "33333333-3333-4333-8333-333333333333"
)

func TestPublishingCarriesCorrelationID(t *testing.T) {
	p := publishing(app.Message{ID: "m-1", Topic: "video.uploaded", Body: []byte(`{}`), CorrelationID: "req-1"})
	if p.CorrelationId != "req-1" || p.MessageId != "m-1" || p.ContentType != "application/json" {
		t.Errorf("publishing = %+v", p)
	}
	if p := publishing(app.Message{ID: "m-2"}); p.CorrelationId != "" {
		t.Errorf("correlation id without one: %q", p.CorrelationId)
	}
}

// ctxHandler records the context of its last call and fails with err.
type ctxHandler struct {
	mu  sync.Mutex
	ctx context.Context
	err error
}

func (h *ctxHandler) Handle(ctx context.Context, _ []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ctx = ctx
	return h.err
}

func (h *ctxHandler) GiveUp(context.Context, []byte, error) error { return nil }

func (h *ctxHandler) lastContext() context.Context {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ctx
}

func TestHandleRestoresCorrelationIntoContext(t *testing.T) {
	var buf bytes.Buffer
	h := &ctxHandler{err: errBoom}
	c, err := newConsumer(ConsumerConfig{
		URL: "amqp://localhost:5672/", Name: "test", Queue: testQueue, Concurrency: 1,
	}, h, &fakeRetrier{}, logging.New(&buf, "worker", slog.LevelInfo))
	if err != nil {
		t.Fatal(err)
	}
	d := delivery(&fakeAck{}, 2)
	d.CorrelationId = "demo-123"
	d.Body = []byte(`{"video_id":"` + testVideoID + `","event_id":"` + testEventID + `"}`)
	c.handle(context.Background(), d)

	if got := logging.RequestID(h.lastContext()); got != "demo-123" {
		t.Errorf("handler context request id = %q, want demo-123", got)
	}
	// The retry warning is logged with every correlation field.
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("log %q: %v", buf.String(), err)
	}
	for k, want := range map[string]any{
		"msg": "message failed; retrying later", "request_id": "demo-123", "message_id": "m-1",
		"attempt": float64(2), "video_id": testVideoID, "event_id": testEventID, "queue": "q",
	} {
		if rec[k] != want {
			t.Errorf("%s = %v, want %v", k, rec[k], want)
		}
	}
}

func TestDeliveryContextIgnoresInvalidValues(t *testing.T) {
	d := amqp.Delivery{
		MessageId:     "m-9",
		CorrelationId: "bad id\nwith newline",
		Body:          []byte(`{"video_id":"not-a-uuid"}`),
	}
	var buf bytes.Buffer
	log := logging.New(&buf, "worker", slog.LevelInfo)
	ctx := deliveryContext(context.Background(), d, 1)
	log.InfoContext(ctx, "x")
	if logging.RequestID(ctx) != "" {
		t.Errorf("invalid correlation id restored: %q", logging.RequestID(ctx))
	}
	out := buf.String()
	if strings.Contains(out, "video_id") || strings.Contains(out, "request_id") || !strings.Contains(out, `"message_id":"m-9"`) {
		t.Errorf("log line %s", out)
	}
	// A body that is not JSON still gets the delivery's fields.
	d.Body = []byte("garbage")
	d.CorrelationId = "ok-1"
	if ctx := deliveryContext(context.Background(), d, 3); logging.RequestID(ctx) != "ok-1" {
		t.Error("request id lost with a malformed body")
	}
}

// TestBrokerCorrelationIDSurvivesRetries publishes a message with a
// correlation id through the real broker and checks the handler's context
// has it on the first attempt and after a retry.
func TestBrokerCorrelationIDSurvivesRetries(t *testing.T) {
	url := brokerURL(t)
	w := testWorkQueue(t, url)
	pub, err := NewPublisher(url, "test-publisher", w.Exchange, nil, w)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()

	var (
		mu  sync.Mutex
		ids []string
	)
	h := &funcHandler{handle: func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		ids = append(ids, logging.RequestID(ctx))
		if len(ids) == 1 {
			return errBoom // retried through the retry queue
		}
		return nil
	}}
	stop := startConsumer(t, url, w, h, pub, time.Second)
	defer stop()

	msg := app.Message{ID: "m-corr", Topic: w.RoutingKey, Body: []byte(`{}`), CorrelationID: "corr-42"}
	if errs := pub.Publish(context.Background(), []app.Message{msg}); errs[0] != nil {
		t.Fatal(errs[0])
	}
	waitFor(t, "the retried message handled", 15*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(ids) == 2
	})
	mu.Lock()
	defer mu.Unlock()
	if ids[0] != "corr-42" || ids[1] != "corr-42" {
		t.Errorf("request ids seen by the handler: %q, want corr-42 twice", ids)
	}
}

type funcHandler struct {
	handle func(ctx context.Context) error
}

func (h *funcHandler) Handle(ctx context.Context, _ []byte) error { return h.handle(ctx) }

func (h *funcHandler) GiveUp(context.Context, []byte, error) error { return nil }
