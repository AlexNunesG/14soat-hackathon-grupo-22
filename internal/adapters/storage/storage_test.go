package storage

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"video-processor/internal/app"
)

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		in         string
		useSSL     bool
		wantHost   string
		wantSecure bool
		wantErr    bool
	}{
		{"storage:8333", false, "storage:8333", false, false},
		{"storage:8333", true, "storage:8333", true, false},
		{" localhost:9000/ ", false, "localhost:9000", false, false},
		{"http://storage:8333", true, "storage:8333", false, false},
		{"https://s3.amazonaws.com", false, "s3.amazonaws.com", true, false},
		{"https://s3.amazonaws.com/", false, "s3.amazonaws.com", true, false},
		{"", false, "", false, true},
		{"ftp://storage:21", false, "", false, true},
		{"http://", false, "", false, true},
		{"http://storage:8333/bucket", false, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			host, secure, err := parseEndpoint(tt.in, tt.useSSL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if host != tt.wantHost || secure != tt.wantSecure {
				t.Errorf("got %q, %v; want %q, %v", host, secure, tt.wantHost, tt.wantSecure)
			}
		})
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Config{Bucket: "videos"}); err == nil {
		t.Error("empty endpoint: want error")
	}
	if _, err := New(Config{Endpoint: "localhost:8333"}); err == nil {
		t.Error("empty bucket: want error")
	}
	c, err := New(Config{Endpoint: "localhost:8333", Bucket: "videos", AccessKey: "a", SecretKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Bucket() != "videos" || c.region != DefaultRegion {
		t.Errorf("bucket %q region %q", c.Bucket(), c.region)
	}
}

// fakeS3 answers the few S3 calls the client makes, for one bucket.
type fakeS3 struct {
	mu       sync.Mutex
	bucket   string
	exists   bool
	objects  map[string]bool
	requests []string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	_, _ = io.Copy(io.Discard, r.Body)

	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	if bucket != f.bucket {
		s3Error(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	switch {
	case key == "" && r.Method == http.MethodHead:
		if !f.exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	case key == "" && r.Method == http.MethodPut:
		f.exists = true
		w.WriteHeader(http.StatusOK)
	case key != "" && r.Method == http.MethodPut:
		if got := r.Header.Get("Content-Type"); got != "video/mp4" {
			s3Error(w, http.StatusBadRequest, "UnexpectedContentType:"+got)
			return
		}
		f.objects[key] = true
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)
	case key != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if !f.objects[key] {
			s3Error(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, "hello")
		}
	case key != "" && r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		s3Error(w, http.StatusNotImplemented, "NotImplemented")
	}
}

func s3Error(w http.ResponseWriter, status int, code string) {
	body, err := xml.Marshal(struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}{Code: code, Message: code})
	if err != nil {
		panic(err)
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func newFake(t *testing.T, exists bool) (*Client, *fakeS3) {
	t.Helper()
	fake := &fakeS3{bucket: "videos", exists: exists, objects: map[string]bool{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, Bucket: "videos", AccessKey: "key", SecretKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	return c, fake
}

func TestPing(t *testing.T) {
	ctx := context.Background()
	c, _ := newFake(t, true)
	if err := c.Ping(ctx); err != nil {
		t.Errorf("existing bucket: %v", err)
	}
	c, _ = newFake(t, false)
	if err := c.Ping(ctx); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing bucket: err = %v", err)
	}
	unreachable, err := New(Config{Endpoint: "127.0.0.1:1", Bucket: "videos"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := unreachable.Ping(ctx); err == nil {
		t.Error("unreachable server: want error")
	}
}

func TestEnsureBucket(t *testing.T) {
	c, fake := newFake(t, false)
	if err := c.EnsureBucket(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fake.exists {
		t.Fatalf("bucket not created; requests: %v", fake.requests)
	}
	// Idempotent: a second call only checks.
	n := len(fake.requests)
	if err := c.EnsureBucket(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.requests[n:]; len(got) != 1 || got[0] != "HEAD /videos/" && got[0] != "HEAD /videos" {
		t.Errorf("second EnsureBucket made %v", got)
	}
}

func TestPutAndGet(t *testing.T) {
	ctx := context.Background()
	c, _ := newFake(t, true)
	if err := c.Put(ctx, "videos/1/in.mp4", strings.NewReader("hello"), 5, "video/mp4"); err != nil {
		t.Fatal(err)
	}
	rc, err := c.Get(ctx, "videos/1/in.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil || string(body) != "hello" {
		t.Errorf("body %q, err %v", body, err)
	}
	if rc.Size != 5 {
		t.Errorf("size %d, want 5", rc.Size)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	c, fake := newFake(t, true)
	if err := c.Put(ctx, "k", strings.NewReader("hello"), 5, "video/mp4"); err != nil {
		t.Fatal(err)
	}
	for range 2 { // a missing key is not an error
		if err := c.Delete(ctx, "k"); err != nil {
			t.Fatal(err)
		}
	}
	if fake.objects["k"] {
		t.Error("object still exists")
	}
	if _, err := c.Get(ctx, "k"); !errors.Is(err, app.ErrObjectNotFound) {
		t.Errorf("Get after Delete: err = %v, want ErrObjectNotFound", err)
	}
}

func TestGetMissingKey(t *testing.T) {
	c, _ := newFake(t, true)
	_, err := c.Get(context.Background(), "nope")
	if !errors.Is(err, app.ErrObjectNotFound) {
		t.Fatalf("err = %v, want ErrObjectNotFound", err)
	}
}

func TestPutError(t *testing.T) {
	c, _ := newFake(t, true)
	// The fake rejects any content type but video/mp4.
	if err := c.Put(context.Background(), "k", strings.NewReader("x"), 1, ""); err == nil {
		t.Fatal("want error")
	}
}
