// Package integration holds black-box integration tests for the video
// processor. Each endpoint has its own *_test.go file.
//
// The tests only talk to the app over HTTP; they never import its code. No
// mocks are used: videos are generated and processed by the real ffmpeg and
// assertions are made on real ZIP/PNG output.
//
// Two modes:
//
//   - Default: TestMain builds the app from the module root, runs it as a
//     separate process on a free port (PORT env var) inside a throwaway
//     working directory, and stops it with SIGTERM at the end. Tests marked
//     with requireReferenceApp also inspect and manipulate that working
//     directory (uploads/, outputs/, temp/) to reach failure paths.
//
//   - BASE_URL=http://host:port: the tests run against an already running
//     implementation. Nothing is built or started, and tests that depend on
//     the reference implementation's filesystem layout are skipped.
//
// Set COVERAGE_OUT=coverage.out (default mode only) to build the app with
// coverage instrumentation and write a profile for `go tool cover`.
//
// Requirements: ffmpeg in PATH (used to generate the test videos).
package integration

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var (
	// baseURL is where the app under test is listening.
	baseURL string

	// referenceDir is the working directory of the app launched by TestMain,
	// or "" when running against an external BASE_URL.
	referenceDir string

	// reference is the app launched by TestMain, nil with BASE_URL.
	reference *referenceApp
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		fmt.Fprintln(os.Stderr, "integration tests require ffmpeg in PATH:", err)
		return 1
	}

	if url := os.Getenv("BASE_URL"); url != "" {
		baseURL = strings.TrimRight(url, "/")
		if err := waitForServer(30 * time.Second); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return m.Run()
	}

	app, err := startReferenceApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	code := m.Run()
	if err := app.stop(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

type referenceApp struct {
	bin      string
	port     string
	cmd      *exec.Cmd
	tmp      string
	coverDir string
	log      *bytes.Buffer
}

func startReferenceApp() (*referenceApp, error) {
	tmp, err := os.MkdirTemp("", "video-processor-it-*")
	if err != nil {
		return nil, err
	}
	app := &referenceApp{tmp: tmp, log: &bytes.Buffer{}}
	fail := func(err error) (*referenceApp, error) {
		os.RemoveAll(tmp)
		return nil, err
	}

	moduleRoot, err := goEnv("GOMOD")
	if err != nil {
		return fail(err)
	}
	moduleRoot = filepath.Dir(moduleRoot)

	bin := filepath.Join(tmp, "app")
	args := []string{"build", "-o", bin}
	if os.Getenv("COVERAGE_OUT") != "" {
		app.coverDir = filepath.Join(tmp, "cover")
		if err := os.Mkdir(app.coverDir, 0755); err != nil {
			return fail(err)
		}
		args = append(args, "-cover")
	}
	build := exec.Command("go", append(args, ".")...)
	build.Dir = moduleRoot
	if out, err := build.CombinedOutput(); err != nil {
		return fail(fmt.Errorf("building the app failed: %v\n%s", err, out))
	}

	port, err := freePort()
	if err != nil {
		return fail(err)
	}

	referenceDir = filepath.Join(tmp, "work")
	if err := os.Mkdir(referenceDir, 0755); err != nil {
		return fail(err)
	}

	app.bin, app.port = bin, port
	app.cmd = app.command(referenceDir, port)
	app.cmd.Stdout = app.log
	app.cmd.Stderr = app.log
	if err := app.cmd.Start(); err != nil {
		return fail(err)
	}

	baseURL = "http://127.0.0.1:" + port
	if err := waitForServer(30 * time.Second); err != nil {
		app.cmd.Process.Kill()
		app.cmd.Wait()
		return fail(fmt.Errorf("%v\napp output:\n%s", err, app.log))
	}
	reference = app
	return app, nil
}

// command prepares another run of the app binary in dir, listening on port.
// An empty port leaves PORT unset so the app falls back to its default.
func (a *referenceApp) command(dir, port string) *exec.Cmd {
	cmd := exec.Command(a.bin)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PORT=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIN_MODE=release")
	if port != "" {
		cmd.Env = append(cmd.Env, "PORT="+port)
	}
	if a.coverDir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+a.coverDir)
	}
	return cmd
}

// stop shuts the app down gracefully (so coverage data is flushed) and
// writes the coverage profile when requested.
func (a *referenceApp) stop() error {
	defer os.RemoveAll(a.tmp)

	if err := a.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- a.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("app did not exit cleanly: %v\napp output:\n%s", err, a.log)
		}
	case <-time.After(30 * time.Second):
		a.cmd.Process.Kill()
		return fmt.Errorf("app did not stop within 30s after SIGTERM")
	}

	if out := os.Getenv("COVERAGE_OUT"); out != "" {
		if !filepath.IsAbs(out) {
			// Relative paths are resolved against the directory `go test` was run from.
			if wd := os.Getenv("PWD"); wd != "" {
				out = filepath.Join(wd, out)
			}
		}
		cmd := exec.Command("go", "tool", "covdata", "textfmt", "-i="+a.coverDir, "-o="+out)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("writing coverage profile: %v\n%s", err, output)
		}
	}
	return nil
}

func goEnv(key string) (string, error) {
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		return "", fmt.Errorf("go env %s: %v", key, err)
	}
	value := strings.TrimSpace(string(out))
	if value == "" || value == os.DevNull {
		return "", fmt.Errorf("go env %s is empty: run the tests inside the module", key)
	}
	return value, nil
}

func freePort() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	return fmt.Sprint(ln.Addr().(*net.TCPAddr).Port), nil
}

func waitForServer(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/status")
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("server at %s did not respond within %s", baseURL, timeout)
}
