package httpapi_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	httpapi "video-processor/internal/adapters/http"
)

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func TestServeShutsDownGracefully(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	ln := listen(t)
	srv := httpapi.NewServer(ln.Addr().String(), mux)

	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- httpapi.Serve(ctx, srv, ln, 5*time.Second) }()

	// An in-flight request...
	type result struct {
		body string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		got <- result{string(b), err}
	}()
	<-started

	// ...is still served after the shutdown signal.
	stop()
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-served:
		t.Fatalf("Serve returned before the in-flight request finished: %v", err)
	default:
	}
	// New connections are refused once shutdown has started.
	if conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		conn.Close()
		t.Error("new connection accepted during shutdown")
	}
	close(release)

	if r := <-got; r.err != nil || r.body != "done" {
		t.Errorf("in-flight request: body %q, err %v", r.body, r.err)
	}
	if err := <-served; err != nil {
		t.Errorf("Serve = %v, want nil after a graceful shutdown", err)
	}
}

func TestServeShutdownTimeout(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	mux := http.NewServeMux()
	mux.HandleFunc("/stuck", func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	})
	ln := listen(t)
	srv := httpapi.NewServer(ln.Addr().String(), mux)
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- httpapi.Serve(ctx, srv, ln, 100*time.Millisecond) }()

	go func() {
		if resp, err := http.Get("http://" + ln.Addr().String() + "/stuck"); err == nil {
			resp.Body.Close()
		}
	}()
	<-started
	stop()
	select {
	case err := <-served:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Serve = %v, want DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not give up after the shutdown timeout")
	}
}

func TestServeReportsServerFailure(t *testing.T) {
	ln := listen(t)
	ln.Close() // Serve fails at once on a closed listener.
	srv := httpapi.NewServer(ln.Addr().String(), http.NotFoundHandler())
	if err := httpapi.Serve(context.Background(), srv, ln, time.Second); err == nil {
		t.Fatal("want error")
	}
}
