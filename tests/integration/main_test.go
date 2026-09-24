// Package integration holds black-box integration tests for the video
// processor: the executable form of the v1 contract in docs/openapi.yaml
// (see docs/adr/0001-replace-legacy-test-contract.md). Each area of the
// contract has its own *_test.go file.
//
// The tests only talk to the running system over HTTP: the API at BASE_URL
// and the MailHog inbox at MAILHOG_URL. They never import its code. No mocks
// are used: videos are generated with the real ffmpeg and processed by the
// real services, and assertions are made on real ZIP/PNG output and real
// e-mails.
//
// Two modes:
//
//   - BASE_URL=http://host:port: the tests run against an already running
//     stack. Nothing is started or stopped. MAILHOG_URL points to the MailHog
//     HTTP API used by the notification tests (default
//     http://localhost:8025).
//
//   - Default (BASE_URL unset): if the compose file exists (COMPOSE_FILE,
//     default deploy/docker-compose.yml; a relative path is resolved against
//     the module root) and docker is available, TestMain runs
//     `docker compose -f <file> up -d --build --wait` before the tests and
//     `docker compose -f <file> down -v` after them (set KEEP_STACK=1 to leave
//     the stack running). The API is then expected at http://localhost:8080
//     and MailHog at http://localhost:8025. Without a compose file or docker,
//     nothing is started: skipped tests pass and any enabled test fails with
//     "no app to test".
//
// In both modes TestMain waits up to 120s for GET /healthz to return 200
// before running the tests, so enabled tests don't race the stack's startup.
// If the API never becomes healthy, enabled tests fail with the reason.
//
// Coverage is not collected here: the services run in containers or
// elsewhere. Code coverage comes from the unit tests.
//
// Requirements: ffmpeg in PATH (used to generate the test videos); docker
// with the compose plugin for the default mode.
package integration

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	// stackBaseURL and stackMailhogURL are where the compose stack publishes
	// the API and the MailHog HTTP API.
	stackBaseURL    = "http://localhost:8080"
	stackMailhogURL = "http://localhost:8025"

	// healthTimeout bounds the wait for GET /healthz before the tests run.
	healthTimeout = 120 * time.Second
)

var (
	// baseURL is where the API under test is listening, or "" when there is
	// no app to test.
	baseURL string

	// mailhogURL is the MailHog HTTP API holding the e-mails sent by the
	// notifier.
	mailhogURL string

	// errNoApp explains why there is no app to test; enabled tests fail with it.
	errNoApp = errors.New("no app to test: set BASE_URL or add deploy/docker-compose.yml")
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		fmt.Fprintln(os.Stderr, "integration tests require ffmpeg in PATH:", err)
		return 1
	}

	mailhogURL = strings.TrimRight(envOr("MAILHOG_URL", stackMailhogURL), "/")

	if url := os.Getenv("BASE_URL"); url != "" {
		baseURL = strings.TrimRight(url, "/")
		waitForApp()
		return m.Run()
	}

	composeFile, err := composeFilePath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if reason := cannotStartStack(composeFile); reason != "" {
		fmt.Fprintf(os.Stderr, "%s: nothing was started, so every enabled test will fail\n", reason)
		return m.Run()
	}

	stack := &composeStack{file: composeFile}
	if err := stack.up(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		stack.down()
		return 1
	}
	baseURL = stackBaseURL
	waitForApp()
	code := m.Run()
	if !stack.down() && code == 0 {
		code = 1
	}
	return code
}

// composeFilePath returns the compose file to start, from COMPOSE_FILE or
// the default under the module root.
func composeFilePath() (string, error) {
	file := envOr("COMPOSE_FILE", filepath.Join("deploy", "docker-compose.yml"))
	if filepath.IsAbs(file) {
		return file, nil
	}
	gomod, err := goEnv("GOMOD")
	if err != nil {
		return "", err
	}
	// `go test` runs in the package directory, so resolve relative paths
	// against the module root, like the Makefile does.
	return filepath.Join(filepath.Dir(gomod), file), nil
}

// cannotStartStack returns why the compose stack cannot be started, or "".
func cannotStartStack(composeFile string) string {
	if _, err := os.Stat(composeFile); err != nil {
		return fmt.Sprintf("no compose file at %s and BASE_URL is not set", composeFile)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Sprintf("docker is not available to start %s and BASE_URL is not set", composeFile)
	}
	return ""
}

// composeStack is the stack started by TestMain with docker compose.
type composeStack struct {
	file string
}

func (s *composeStack) up() error {
	if err := s.compose("up", "-d", "--build", "--wait"); err != nil {
		return fmt.Errorf("starting the stack from %s failed: %w", s.file, err)
	}
	return nil
}

// down removes the stack and its volumes unless KEEP_STACK=1. It reports
// whether that succeeded.
func (s *composeStack) down() bool {
	if os.Getenv("KEEP_STACK") == "1" {
		fmt.Fprintf(os.Stderr, "KEEP_STACK=1: leaving the stack from %s running\n", s.file)
		return true
	}
	if err := s.compose("down", "-v"); err != nil {
		fmt.Fprintf(os.Stderr, "stopping the stack from %s failed: %v\n", s.file, err)
		return false
	}
	return true
}

// compose runs `docker compose -f <file> args...`, streaming its output to
// stderr so slow image builds show progress.
func (s *composeStack) compose(args ...string) error {
	cmd := exec.Command("docker", append([]string{"compose", "-f", s.file}, args...)...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// waitForApp waits until GET /healthz returns 200. On timeout the app is
// marked as unavailable, so enabled tests fail with the reason instead of
// each one waiting or reporting a confusing error.
func waitForApp() {
	if err := waitForHealthy(baseURL, healthTimeout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		errNoApp = err
		baseURL = ""
	}
}

func waitForHealthy(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("no app to test: GET %s/healthz did not return 200 within %s (last error: %w)", url, timeout, lastErr)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func goEnv(key string) (string, error) {
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		return "", fmt.Errorf("go env %s: %w", key, err)
	}
	value := strings.TrimSpace(string(out))
	if value == "" || value == os.DevNull {
		return "", fmt.Errorf("go env %s is empty: run the tests inside the module", key)
	}
	return value, nil
}
