package domain_test

import (
	"testing"

	"video-processor/internal/domain"
)

var allStatuses = []domain.VideoStatus{
	domain.StatusPending, domain.StatusProcessing, domain.StatusDone, domain.StatusFailed,
}

func TestParseVideoStatus(t *testing.T) {
	for _, s := range allStatuses {
		got, err := domain.ParseVideoStatus(s.String())
		if err != nil || got != s {
			t.Errorf("ParseVideoStatus(%q) = %q, %v", s, got, err)
		}
	}
	for _, bad := range []string{"", "pending", "RUNNING", "DONE "} {
		if _, err := domain.ParseVideoStatus(bad); err == nil {
			t.Errorf("ParseVideoStatus(%q): want error", bad)
		}
	}
}

func TestVideoStatusIsFinal(t *testing.T) {
	want := map[domain.VideoStatus]bool{
		domain.StatusPending:    false,
		domain.StatusProcessing: false,
		domain.StatusDone:       true,
		domain.StatusFailed:     true,
	}
	for s, final := range want {
		if got := s.IsFinal(); got != final {
			t.Errorf("%s.IsFinal() = %v, want %v", s, got, final)
		}
	}
}

func TestVideoStatusCanTransitionTo(t *testing.T) {
	allowed := map[[2]domain.VideoStatus]bool{
		{domain.StatusPending, domain.StatusProcessing}: true,
		{domain.StatusPending, domain.StatusFailed}:     true,
		{domain.StatusProcessing, domain.StatusDone}:    true,
		{domain.StatusProcessing, domain.StatusFailed}:  true,
	}
	// Every pair not listed above is rejected, including self-transitions
	// and anything leaving a final status.
	for _, from := range allStatuses {
		for _, to := range allStatuses {
			want := allowed[[2]domain.VideoStatus{from, to}]
			if got := from.CanTransitionTo(to); got != want {
				t.Errorf("%s -> %s: got %v, want %v", from, to, got, want)
			}
		}
		if from.CanTransitionTo("BOGUS") {
			t.Errorf("%s -> BOGUS allowed", from)
		}
	}
	if domain.VideoStatus("BOGUS").CanTransitionTo(domain.StatusDone) {
		t.Error("BOGUS -> DONE allowed")
	}
}
