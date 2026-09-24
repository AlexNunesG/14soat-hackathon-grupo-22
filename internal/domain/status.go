package domain

import "fmt"

// VideoStatus is where a video is in its processing lifecycle:
//
//	PENDING -> PROCESSING -> DONE | FAILED
//
// DONE and FAILED are final. A video may also fail before a worker starts
// it (PENDING -> FAILED), e.g. when its message is dead-lettered.
type VideoStatus string

// The video statuses, as they appear in the API and the database.
const (
	StatusPending    VideoStatus = "PENDING"
	StatusProcessing VideoStatus = "PROCESSING"
	StatusDone       VideoStatus = "DONE"
	StatusFailed     VideoStatus = "FAILED"
)

// transitions lists the valid next statuses of each non-final status.
var transitions = map[VideoStatus][]VideoStatus{
	StatusPending:    {StatusProcessing, StatusFailed},
	StatusProcessing: {StatusDone, StatusFailed},
}

// ParseVideoStatus returns the status named s (exact, uppercase), or an
// error.
func ParseVideoStatus(s string) (VideoStatus, error) {
	st := VideoStatus(s)
	if !st.IsValid() {
		return "", fmt.Errorf("invalid video status %q", s)
	}
	return st, nil
}

// String returns the status name.
func (s VideoStatus) String() string { return string(s) }

// IsValid reports whether s is one of the four statuses.
func (s VideoStatus) IsValid() bool {
	switch s {
	case StatusPending, StatusProcessing, StatusDone, StatusFailed:
		return true
	}
	return false
}

// IsFinal reports whether s is DONE or FAILED: no further transition is
// possible.
func (s VideoStatus) IsFinal() bool {
	return s == StatusDone || s == StatusFailed
}

// CanTransitionTo reports whether a video in status s may move to next.
func (s VideoStatus) CanTransitionTo(next VideoStatus) bool {
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}
