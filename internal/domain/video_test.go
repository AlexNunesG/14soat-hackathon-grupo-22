package domain_test

import (
	"errors"
	"testing"
	"time"

	"video-processor/internal/domain"
)

var (
	t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Second)
	t2 = t0.Add(2 * time.Second)
)

func newPending(t *testing.T) *domain.Video {
	t.Helper()
	v, err := domain.NewVideo("vid-1", "user-1", "holiday.mp4", "videos/vid-1/holiday.mp4", t0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNewVideo(t *testing.T) {
	v := newPending(t)
	if v.Status != domain.StatusPending || !v.CreatedAt.Equal(t0) || !v.UpdatedAt.Equal(t0) {
		t.Errorf("unexpected new video: %+v", v)
	}
	if v.FrameCount != 0 || v.ErrorMessage != "" || v.ZipKey != "" {
		t.Errorf("new video has results: %+v", v)
	}
	if got := v.ZipName(); got != "holiday_frames.zip" {
		t.Errorf("ZipName() = %q", got)
	}
}

func TestNewVideoRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name                         string
		id, owner, original, storage string
		wantUnsupported              bool
	}{
		{"empty id", "", "u", "a.mp4", "k", false},
		{"empty owner", "v", " ", "a.mp4", "k", false},
		{"empty name", "v", "u", "", "k", false},
		{"empty key", "v", "u", "a.mp4", "", false},
		{"no extension", "v", "u", "clip", "k", true},
		{"trailing dot", "v", "u", "clip.", "k", true},
		{"double extension", "v", "u", "clip.mp4.exe", "k", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewVideo(tt.id, tt.owner, tt.original, tt.storage, t0)
			if !errors.Is(err, domain.ErrInvalidVideo) {
				t.Fatalf("err = %v, want ErrInvalidVideo", err)
			}
			if got := errors.Is(err, domain.ErrUnsupportedFormat); got != tt.wantUnsupported {
				t.Errorf("errors.Is(err, ErrUnsupportedFormat) = %v, want %v", got, tt.wantUnsupported)
			}
		})
	}
}

func TestVideoLifecycleDone(t *testing.T) {
	v := newPending(t)
	if err := v.Start(t1); err != nil {
		t.Fatal(err)
	}
	if v.Status != domain.StatusProcessing || !v.UpdatedAt.Equal(t1) {
		t.Fatalf("after Start: %+v", v)
	}
	if err := v.Complete("zips/vid-1.zip", 3, t2); err != nil {
		t.Fatal(err)
	}
	if v.Status != domain.StatusDone || v.FrameCount != 3 || v.ZipKey != "zips/vid-1.zip" || !v.UpdatedAt.Equal(t2) {
		t.Fatalf("after Complete: %+v", v)
	}
	if !v.CreatedAt.Equal(t0) {
		t.Errorf("CreatedAt changed: %v", v.CreatedAt)
	}
}

func TestVideoLifecycleFailed(t *testing.T) {
	for _, start := range []bool{true, false} {
		v := newPending(t)
		if start {
			if err := v.Start(t1); err != nil {
				t.Fatal(err)
			}
		}
		if err := v.Fail("  no video stream found in input\n", t2); err != nil {
			t.Fatalf("Fail (started=%v): %v", start, err)
		}
		if v.Status != domain.StatusFailed || v.ErrorMessage != "no video stream found in input" {
			t.Errorf("after Fail (started=%v): %+v", start, v)
		}
	}
}

func TestVideoRejectsInvalidChanges(t *testing.T) {
	tests := []struct {
		name  string
		setup func(v *domain.Video) error
		act   func(v *domain.Video) error
		want  error
	}{
		{
			name: "complete while pending",
			act:  func(v *domain.Video) error { return v.Complete("z", 1, t1) },
			want: domain.ErrInvalidTransition,
		},
		{
			name:  "start twice",
			setup: func(v *domain.Video) error { return v.Start(t1) },
			act:   func(v *domain.Video) error { return v.Start(t2) },
			want:  domain.ErrInvalidTransition,
		},
		{
			name: "restart when done",
			setup: func(v *domain.Video) error {
				if err := v.Start(t1); err != nil {
					return err
				}
				return v.Complete("z", 1, t1)
			},
			act:  func(v *domain.Video) error { return v.Start(t2) },
			want: domain.ErrInvalidTransition,
		},
		{
			name: "fail when done",
			setup: func(v *domain.Video) error {
				if err := v.Start(t1); err != nil {
					return err
				}
				return v.Complete("z", 1, t1)
			},
			act:  func(v *domain.Video) error { return v.Fail("boom", t2) },
			want: domain.ErrInvalidTransition,
		},
		{
			name:  "complete with zero frames",
			setup: func(v *domain.Video) error { return v.Start(t1) },
			act:   func(v *domain.Video) error { return v.Complete("z", 0, t2) },
			want:  domain.ErrInvalidVideo,
		},
		{
			name:  "complete without zip key",
			setup: func(v *domain.Video) error { return v.Start(t1) },
			act:   func(v *domain.Video) error { return v.Complete(" ", 2, t2) },
			want:  domain.ErrInvalidVideo,
		},
		{
			name:  "fail without reason",
			setup: func(v *domain.Video) error { return v.Start(t1) },
			act:   func(v *domain.Video) error { return v.Fail(" \n", t2) },
			want:  domain.ErrInvalidVideo,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newPending(t)
			if tt.setup != nil {
				if err := tt.setup(v); err != nil {
					t.Fatal(err)
				}
			}
			before := *v
			if err := tt.act(v); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if *v != before {
				t.Errorf("video changed on error:\n got %+v\nwant %+v", *v, before)
			}
		})
	}
}
