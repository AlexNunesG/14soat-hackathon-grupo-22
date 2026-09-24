package rabbitmq

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

var update = flag.Bool("update", false, "rewrite deploy/rabbitmq/definitions.json from the code")

// definitionsFile is the topology exported for manual import (D2).
var definitionsFile = filepath.Join("..", "..", "..", "deploy", "rabbitmq", "definitions.json")

// TestDefinitionsFileMatchesTopology keeps deploy/rabbitmq/definitions.json
// in sync with the topology the services declare. Regenerate it with
//
//	go test ./internal/adapters/rabbitmq/ -run TestDefinitionsFile -update
func TestDefinitionsFileMatchesTopology(t *testing.T) {
	want, err := json.MarshalIndent(NewDefinitions(VideoProcess), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if *update {
		if err := os.WriteFile(definitionsFile, want, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(definitionsFile)
	if err != nil {
		t.Fatal(err)
	}
	var gotJSON, wantJSON any
	if err := json.Unmarshal(got, &gotJSON); err != nil {
		t.Fatalf("%s: %v", definitionsFile, err)
	}
	if err := json.Unmarshal(want, &wantJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Errorf("%s is out of date; regenerate it with -update. Want:\n%s", definitionsFile, want)
	}
}

func TestVideoProcessTopology(t *testing.T) {
	w := VideoProcess
	if w.MaxAttempts() != 4 || w.RetryQueue(1) != "video.process.retry.1" || w.DeadLetterQueue() != "video.process.dlq" {
		t.Errorf("max attempts %d, retry queue %q, DLQ %q", w.MaxAttempts(), w.RetryQueue(1), w.DeadLetterQueue())
	}
	d := NewDefinitions(w)
	var queues []string
	for _, q := range d.Queues {
		queues = append(queues, q.Name)
		if !q.Durable || q.AutoDelete {
			t.Errorf("queue %s must be durable and not auto-delete", q.Name)
		}
	}
	want := []string{"video.process", "video.process.dlq", "video.process.retry.1", "video.process.retry.2", "video.process.retry.3"}
	if !slices.Equal(queues, want) {
		t.Errorf("queues %v, want %v", queues, want)
	}
	main := d.Queues[0].Arguments
	if main["x-dead-letter-exchange"] != "videos.dlx" || main["x-dead-letter-routing-key"] != "video.process" {
		t.Errorf("work queue arguments %v", main)
	}
	for i, delay := range w.RetryDelays {
		args := d.Queues[2+i].Arguments
		if args["x-message-ttl"] != delay.Milliseconds() || args["x-dead-letter-exchange"] != "" ||
			args["x-dead-letter-routing-key"] != "video.process" {
			t.Errorf("retry queue %d arguments %v", i+1, args)
		}
	}
	if len(d.Exchanges) != 2 || d.Exchanges[0].Type != "topic" || d.Exchanges[1].Type != "direct" {
		t.Errorf("exchanges %+v", d.Exchanges)
	}
	wantBindings := []DefinitionsBinding{
		{Source: "videos", VHost: "/", Destination: "video.process", DestinationType: "queue", RoutingKey: "video.uploaded", Arguments: map[string]any{}},
		{Source: "videos.dlx", VHost: "/", Destination: "video.process.dlq", DestinationType: "queue", RoutingKey: "video.process", Arguments: map[string]any{}},
	}
	if !reflect.DeepEqual(d.Bindings, wantBindings) {
		t.Errorf("bindings %+v", d.Bindings)
	}
}

func TestDefinitionsDeduplicateSharedExchanges(t *testing.T) {
	other := WorkQueue{Exchange: "videos", RoutingKey: "video.failed", Queue: "video.notify", DeadLetterExchange: "videos.dlx"}
	d := NewDefinitions(VideoProcess, other)
	if len(d.Exchanges) != 2 {
		t.Errorf("exchanges %+v, want each once", d.Exchanges)
	}
	if len(d.Queues) != 5+2 {
		t.Errorf("%d queues, want 7", len(d.Queues))
	}
}

// recordingDeclarer records the declarations, failing on failOn.
type recordingDeclarer struct {
	calls  []string
	failOn string
}

func (r *recordingDeclarer) record(call string) error {
	r.calls = append(r.calls, call)
	if call == r.failOn {
		return fmt.Errorf("PRECONDITION_FAILED on %s", call)
	}
	return nil
}

func (r *recordingDeclarer) ExchangeDeclare(name, kind string, durable, autoDelete, internal, _ bool, _ amqp.Table) error {
	if !durable || autoDelete || internal {
		return fmt.Errorf("exchange %s: durable %v autoDelete %v internal %v", name, durable, autoDelete, internal)
	}
	return r.record("exchange " + name + " " + kind)
}

func (r *recordingDeclarer) QueueDeclare(name string, durable, autoDelete, exclusive, _ bool, _ amqp.Table) (amqp.Queue, error) {
	if !durable || autoDelete || exclusive {
		return amqp.Queue{}, fmt.Errorf("queue %s: durable %v autoDelete %v exclusive %v", name, durable, autoDelete, exclusive)
	}
	return amqp.Queue{Name: name}, r.record("queue " + name)
}

func (r *recordingDeclarer) QueueBind(name, key, exchange string, _ bool, _ amqp.Table) error {
	return r.record("bind " + name + " " + key + " " + exchange)
}

func TestDeclare(t *testing.T) {
	w := WorkQueue{Exchange: "ex", RoutingKey: "k", Queue: "q", DeadLetterExchange: "dlx", RetryDelays: []time.Duration{time.Second}}
	r := &recordingDeclarer{}
	if err := declare(r, w); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"exchange ex topic", "exchange dlx direct",
		"queue q", "queue q.dlq", "queue q.retry.1",
		"bind q k ex", "bind q.dlq q dlx",
	}
	if !slices.Equal(r.calls, want) {
		t.Errorf("calls %v\nwant  %v", r.calls, want)
	}
	for _, failOn := range want {
		if err := declare(&recordingDeclarer{failOn: failOn}, w); err == nil {
			t.Errorf("failure on %q not reported", failOn)
		}
	}
}
