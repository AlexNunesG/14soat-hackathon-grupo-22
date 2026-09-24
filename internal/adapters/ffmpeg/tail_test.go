package ffmpeg

import (
	"errors"
	"strings"
	"testing"
)

func TestTailBufferKeepsTheEnd(t *testing.T) {
	b := &tailBuffer{max: 8}
	for _, s := range []string{"abc", "defgh", "ijklmnop", "q"} {
		if n, err := b.Write([]byte(s)); err != nil || n != len(s) {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	if got := b.String(); got != "jklmnopq" {
		t.Errorf("String() = %q, want %q", got, "jklmnopq")
	}
}

func TestNewRunErrorUsesLastLine(t *testing.T) {
	cause := errors.New("exit status 1")
	err := newRunError("first\nError opening input files: Invalid data found when processing input\n\n", cause)
	if want := "ffmpeg: Error opening input files: Invalid data found when processing input"; err.Error() != want {
		t.Errorf("message %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, cause) {
		t.Error("the exec error is not wrapped")
	}
	if err := newRunError("  ", cause); !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("empty stderr: message %q", err.Error())
	}
}
