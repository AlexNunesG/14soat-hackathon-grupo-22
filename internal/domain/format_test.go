package domain_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"video-processor/internal/domain"
)

func TestSupportedFormats(t *testing.T) {
	want := []string{"mp4", "avi", "mov", "mkv", "wmv", "flv", "webm"}
	got := domain.SupportedFormats()
	if !slices.Equal(got, want) {
		t.Fatalf("SupportedFormats() = %v, want %v", got, want)
	}
	got[0] = "exe"
	if domain.SupportedFormats()[0] != "mp4" {
		t.Error("SupportedFormats returned the internal slice")
	}
}

func TestExtension(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		wantOK bool
	}{
		{"clip.mp4", "mp4", true},
		{"CLIP.MP4", "mp4", true},
		{"clip.mp4.exe", "exe", true},
		{"my.holiday.Mkv", "mkv", true},
		{".webm", "webm", true},
		{"clip", "", false},
		{"clip.", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := domain.Extension(tt.name)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("Extension(%q) = %q, %v; want %q, %v", tt.name, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestIsSupportedFormat(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"clip.mp4", true},
		{"clip.avi", true},
		{"clip.mov", true},
		{"clip.mkv", true},
		{"clip.wmv", true},
		{"clip.flv", true},
		{"clip.webm", true},
		{"UPPER CASE.MP4", true},
		{"vídeo com acentuação.Mkv", true},
		{"MiXeD.WeBm", true},
		{"archive.tar.mov", true},
		{"clip", false},
		{"clip.", false},
		{"clip.mp4.exe", false},
		{"notes.txt", false},
		{"clip.mpeg", false},
		{"clip.mp", false},
		{"clip.mp4 ", false},
		{"mp4", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.IsSupportedFormat(tt.name); got != tt.want {
				t.Errorf("IsSupportedFormat(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestValidateFormat(t *testing.T) {
	if err := domain.ValidateFormat("clip.MOV"); err != nil {
		t.Fatalf("ValidateFormat(clip.MOV) = %v, want nil", err)
	}
	err := domain.ValidateFormat("notes.txt")
	if !errors.Is(err, domain.ErrUnsupportedFormat) {
		t.Fatalf("ValidateFormat(notes.txt) = %v, want ErrUnsupportedFormat", err)
	}
	const want = `unsupported file format "notes.txt"; supported formats: mp4, avi, mov, mkv, wmv, flv, webm`
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
	for _, f := range domain.SupportedFormats() {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("message %q does not list %s", err.Error(), f)
		}
	}
}
