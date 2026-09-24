package integration

// Integration tests for the app process itself: startup and shutdown.
// They launch extra instances of the reference binary, so they are skipped
// when BASE_URL is set.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestStartupCreatesWorkingDirectories(t *testing.T) {
	notImplemented(t)
	requireReferenceApp(t)

	for _, dir := range []string{"uploads", "outputs", "temp"} {
		info, err := os.Stat(filepath.Join(referenceDir, dir))
		if err != nil || !info.IsDir() {
			t.Errorf("expected %s/ to be created at startup (err=%v)", dir, err)
		}
	}
}

func TestStartupFailsWhenPortIsInUse(t *testing.T) {
	notImplemented(t)
	requireReferenceApp(t)

	// The reference app already listens on this port.
	cmd := reference.command(t.TempDir(), reference.port)
	out, err := runWithTimeout(t, cmd, 30*time.Second)

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("expected a non-zero exit, got err=%v\n%s", err, out)
	}
	if !strings.Contains(out, "address already in use") {
		t.Errorf("expected the bind error in the output, got:\n%s", out)
	}
}

func TestStartupDefaultsToPort8080(t *testing.T) {
	notImplemented(t)
	requireReferenceApp(t)

	cmd := reference.command(t.TempDir(), "")
	output := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Either it starts on 8080, or 8080 is taken on this machine and it fails
	// to bind it. Both show that 8080 is the default.
	deadline := time.After(30 * time.Second)
	for {
		select {
		case <-done:
			if !strings.Contains(output.String(), ":8080") {
				t.Fatalf("app exited without trying port 8080:\n%s", output.String())
			}
			return
		case <-deadline:
			cmd.Process.Kill()
			t.Fatalf("app neither started nor failed within 30s:\n%s", output.String())
		case <-time.After(50 * time.Millisecond):
			if strings.Contains(output.String(), "Servidor iniciado na porta 8080") {
				cmd.Process.Signal(syscall.SIGTERM)
				if err := <-done; err != nil {
					t.Errorf("expected a clean exit after SIGTERM, got %v", err)
				}
				return
			}
		}
	}
}

func TestShutdownIsGracefulOnSIGTERM(t *testing.T) {
	notImplemented(t)
	requireReferenceApp(t)

	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	cmd := reference.command(t.TempDir(), port)
	output := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })

	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(output.String(), "Servidor iniciado na porta "+port) {
		if time.Now().After(deadline) {
			t.Fatalf("second instance did not start:\n%s", output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected a clean exit after SIGTERM, got %v\n%s", err, output.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("app did not stop within 30s after SIGTERM")
	}
	if !strings.Contains(output.String(), "Servidor encerrado") {
		t.Errorf("expected shutdown message, got:\n%s", output.String())
	}
}

func runWithTimeout(t *testing.T, cmd *exec.Cmd, timeout time.Duration) (string, error) {
	t.Helper()
	var output strings.Builder
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return output.String(), err
	case <-time.After(timeout):
		cmd.Process.Kill()
		<-done
		t.Fatalf("app did not exit within %s:\n%s", timeout, output.String())
		return "", nil
	}
}

// syncBuffer lets the test read a process's output while it is being written.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
