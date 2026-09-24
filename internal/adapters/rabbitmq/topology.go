package rabbitmq

import (
	"fmt"
	"strconv"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/app"
)

// Names of the video processing topology.
const (
	// ExchangeVideos is the topic exchange every video event is published to.
	ExchangeVideos = "videos"
	// ExchangeVideosDLX is the direct exchange dead-lettered messages go
	// through, routed by the name of the queue they died in.
	ExchangeVideosDLX = "videos.dlx"
	// QueueVideoProcess is the workers' queue of processing jobs.
	QueueVideoProcess = "video.process"
)

// VideoProcess is the processing work queue: jobs published to "videos"
// with the key video.uploaded, retried after 2 s, 10 s and 30 s (so at
// most 4 attempts), then dead-lettered to video.process.dlq.
var VideoProcess = WorkQueue{
	Exchange:           ExchangeVideos,
	RoutingKey:         app.TopicVideoUploaded,
	Queue:              QueueVideoProcess,
	DeadLetterExchange: ExchangeVideosDLX,
	RetryDelays:        []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second},
}

// WorkQueue is a durable work queue bound to a topic exchange, with one
// delay queue per retry and a dead-letter queue:
//
//	Exchange (topic) --RoutingKey--> Queue --(consumer nack, no requeue)--> DeadLetterExchange --Queue--> Queue.dlq
//	consumer --(retry n, via the default exchange)--> Queue.retry.n --(TTL RetryDelays[n-1] expires)--> Queue
//
// A retry queue has no consumer: its messages wait for the queue's TTL and
// are then dead-lettered back to Queue through the default exchange, so
// only Queue gets them again (not every queue bound to RoutingKey). One
// queue per delay avoids the head-of-line blocking of per-message TTLs.
type WorkQueue struct {
	Exchange           string
	RoutingKey         string
	Queue              string
	DeadLetterExchange string
	RetryDelays        []time.Duration
}

// RetryQueue is the name of the queue holding messages before their n-th
// retry (1-based).
func (w WorkQueue) RetryQueue(n int) string { return w.Queue + ".retry." + strconv.Itoa(n) }

// DeadLetterQueue is the name of the queue holding the messages that were
// given up on.
func (w WorkQueue) DeadLetterQueue() string { return w.Queue + ".dlq" }

// MaxAttempts is the number of deliveries a message gets before it is
// dead-lettered: the first plus one per retry delay.
func (w WorkQueue) MaxAttempts() int { return len(w.RetryDelays) + 1 }

// exchange, queue and binding describe the resources of a topology.
type exchange struct {
	Name string
	Kind string
}

type queue struct {
	Name string
	Args amqp.Table
}

type binding struct {
	Queue, Exchange, Key string
}

// resources returns what the work queue needs, in declaration order.
func (w WorkQueue) resources() ([]exchange, []queue, []binding) {
	exchanges := []exchange{
		{Name: w.Exchange, Kind: amqp.ExchangeTopic},
		{Name: w.DeadLetterExchange, Kind: amqp.ExchangeDirect},
	}
	queues := []queue{
		{Name: w.Queue, Args: amqp.Table{
			"x-queue-type":              "classic",
			"x-dead-letter-exchange":    w.DeadLetterExchange,
			"x-dead-letter-routing-key": w.Queue,
		}},
		{Name: w.DeadLetterQueue(), Args: amqp.Table{"x-queue-type": "classic"}},
	}
	for i, d := range w.RetryDelays {
		queues = append(queues, queue{Name: w.RetryQueue(i + 1), Args: amqp.Table{
			"x-queue-type":              "classic",
			"x-message-ttl":             d.Milliseconds(),
			"x-dead-letter-exchange":    "", // the default exchange: straight back to Queue
			"x-dead-letter-routing-key": w.Queue,
		}})
	}
	bindings := []binding{
		{Queue: w.Queue, Exchange: w.Exchange, Key: w.RoutingKey},
		{Queue: w.DeadLetterQueue(), Exchange: w.DeadLetterExchange, Key: w.Queue},
	}
	return exchanges, queues, bindings
}

// declarer is the part of *amqp.Channel that declares resources.
type declarer interface {
	ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error
	QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
	QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
}

// declare declares the work queues' durable exchanges, queues and
// bindings. It is idempotent: every service declares the topology it uses
// whenever it connects, so the broker needs no manual setup. Declaring a
// resource that exists with other arguments fails (PRECONDITION_FAILED).
func declare(ch declarer, queues ...WorkQueue) error {
	for _, w := range queues {
		exchanges, qs, bindings := w.resources()
		for _, e := range exchanges {
			if err := ch.ExchangeDeclare(e.Name, e.Kind, true, false, false, false, nil); err != nil {
				return fmt.Errorf("rabbitmq: declare exchange %s: %w", e.Name, err)
			}
		}
		for _, q := range qs {
			if _, err := ch.QueueDeclare(q.Name, true, false, false, false, q.Args); err != nil {
				return fmt.Errorf("rabbitmq: declare queue %s: %w", q.Name, err)
			}
		}
		for _, b := range bindings {
			if err := ch.QueueBind(b.Queue, b.Key, b.Exchange, false, nil); err != nil {
				return fmt.Errorf("rabbitmq: bind queue %s to %s: %w", b.Queue, b.Exchange, err)
			}
		}
	}
	return nil
}

// Definitions is the topology in the format of RabbitMQ's definitions
// export/import (rabbitmqctl import_definitions, management UI), for the
// default vhost "/". deploy/rabbitmq/definitions.json holds it.
type Definitions struct {
	Exchanges []DefinitionsExchange `json:"exchanges"`
	Queues    []DefinitionsQueue    `json:"queues"`
	Bindings  []DefinitionsBinding  `json:"bindings"`
}

// DefinitionsExchange is an exchange in Definitions.
type DefinitionsExchange struct {
	Name       string         `json:"name"`
	VHost      string         `json:"vhost"`
	Type       string         `json:"type"`
	Durable    bool           `json:"durable"`
	AutoDelete bool           `json:"auto_delete"`
	Internal   bool           `json:"internal"`
	Arguments  map[string]any `json:"arguments"`
}

// DefinitionsQueue is a queue in Definitions.
type DefinitionsQueue struct {
	Name       string         `json:"name"`
	VHost      string         `json:"vhost"`
	Durable    bool           `json:"durable"`
	AutoDelete bool           `json:"auto_delete"`
	Arguments  map[string]any `json:"arguments"`
}

// DefinitionsBinding is a binding in Definitions.
type DefinitionsBinding struct {
	Source          string         `json:"source"`
	VHost           string         `json:"vhost"`
	Destination     string         `json:"destination"`
	DestinationType string         `json:"destination_type"`
	RoutingKey      string         `json:"routing_key"`
	Arguments       map[string]any `json:"arguments"`
}

// NewDefinitions returns the definitions of the work queues, without
// duplicates (queues may share exchanges).
func NewDefinitions(queues ...WorkQueue) Definitions {
	const vhost = "/"
	d := Definitions{
		Exchanges: []DefinitionsExchange{},
		Queues:    []DefinitionsQueue{},
		Bindings:  []DefinitionsBinding{},
	}
	seen := map[string]bool{}
	for _, w := range queues {
		exchanges, qs, bindings := w.resources()
		for _, e := range exchanges {
			if seen["exchange "+e.Name] {
				continue
			}
			seen["exchange "+e.Name] = true
			d.Exchanges = append(d.Exchanges, DefinitionsExchange{
				Name: e.Name, VHost: vhost, Type: e.Kind, Durable: true, Arguments: map[string]any{},
			})
		}
		for _, q := range qs {
			d.Queues = append(d.Queues, DefinitionsQueue{
				Name: q.Name, VHost: vhost, Durable: true, Arguments: map[string]any(q.Args),
			})
		}
		for _, b := range bindings {
			d.Bindings = append(d.Bindings, DefinitionsBinding{
				Source: b.Exchange, VHost: vhost, Destination: b.Queue, DestinationType: "queue",
				RoutingKey: b.Key, Arguments: map[string]any{},
			})
		}
	}
	return d
}
