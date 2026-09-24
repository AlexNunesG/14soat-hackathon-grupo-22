package domain_test

import (
	"testing"

	"video-processor/internal/domain"
)

func TestFrameName(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{1, "frame_0001.png"},
		{42, "frame_0042.png"},
		{9999, "frame_9999.png"},
	}
	for _, tt := range tests {
		if got := domain.FrameName(tt.n); got != tt.want {
			t.Errorf("FrameName(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestZipName(t *testing.T) {
	tests := []struct {
		name, want string
	}{
		{"holiday.mp4", "holiday_frames.zip"},
		{"Trip.MKV", "Trip_frames.zip"},
		{"UPPER CASE.MP4", "UPPER CASE_frames.zip"},
		{"my.holiday.webm", "my.holiday_frames.zip"},
		{"vídeo com acentuação.Mkv", "vídeo com acentuação_frames.zip"},
		{"clip", "clip_frames.zip"},
		{".mp4", "video_frames.zip"},
		{"", "video_frames.zip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.ZipName(tt.name); got != tt.want {
				t.Errorf("ZipName(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got := domain.NormalizeEmail("  Ada@Example.COM "); got != "ada@example.com" {
		t.Errorf("NormalizeEmail = %q", got)
	}
}
