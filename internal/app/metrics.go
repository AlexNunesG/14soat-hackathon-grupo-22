package app

import "time"

// Outcome is how a message (a worker job or a notifier event) was finished,
// as reported to the metrics ports (docs/observability.md, "Metrics"). Each
// delivery gets exactly one outcome: the use case reports the ones it
// decides when its handler returns nil (done, failed, ignored, sent,
// duplicate), and the consumer the ones it decides when the handler
// returns an error (retried, dead_lettered, requeued).
type Outcome string

// Outcomes of worker jobs.
const (
	// OutcomeDone: the video ended DONE.
	OutcomeDone Outcome = "done"
	// OutcomeFailed: the video ended FAILED because of its content (it
	// cannot be decoded, has no frames, timed out, is missing).
	OutcomeFailed Outcome = "failed"
)

// Outcomes of notifier events.
const (
	// OutcomeSent: the e-mail was sent.
	OutcomeSent Outcome = "sent"
	// OutcomeDuplicate: the e-mail of the event had already been sent.
	OutcomeDuplicate Outcome = "duplicate"
)

// Outcomes of both.
const (
	// OutcomeIgnored: there was nothing to do (a job for an unknown or
	// already final video, or a run that lost the race to finish it; an
	// event that is not notified).
	OutcomeIgnored Outcome = "ignored"
	// OutcomeRetried: the handler failed and the message was scheduled for
	// another attempt.
	OutcomeRetried Outcome = "retried"
	// OutcomeDeadLettered: the message went to its dead-letter queue
	// (retries exhausted, malformed, or failed for good).
	OutcomeDeadLettered Outcome = "dead_lettered"
	// OutcomeRequeued: the message was put back in its queue (shutdown, or
	// its outcome could not be recorded).
	OutcomeRequeued Outcome = "requeued"
)

// OutcomeRecorder is the metrics port of message handling.
type OutcomeRecorder interface {
	// RecordOutcome records a message finished with outcome after d.
	RecordOutcome(outcome Outcome, d time.Duration)
}

// ProcessorMetrics is the metrics port of the worker's use case.
type ProcessorMetrics interface {
	OutcomeRecorder
	// FramesExtracted records the frames of a video that ended DONE.
	FramesExtracted(n int)
}

// noMetrics is the default metrics port: it records nothing.
type noMetrics struct{}

func (noMetrics) RecordOutcome(Outcome, time.Duration) {}
func (noMetrics) FramesExtracted(int)                  {}
