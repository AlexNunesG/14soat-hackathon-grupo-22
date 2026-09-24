package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"video-processor/internal/app"
)

// TestAgainstRealS3 runs the client against a real S3-compatible server
// when STORAGE_TEST_ENDPOINT is set, e.g. the compose stack's SeaweedFS:
//
//	STORAGE_TEST_ENDPOINT=localhost:8333 STORAGE_TEST_ACCESS_KEY=... \
//	STORAGE_TEST_SECRET_KEY=... go test ./internal/adapters/storage/
//
// It creates its own bucket, so it does not touch the application's data.
func TestAgainstRealS3(t *testing.T) {
	endpoint := os.Getenv("STORAGE_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("STORAGE_TEST_ENDPOINT not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	c, err := New(Config{
		Endpoint:  endpoint,
		AccessKey: os.Getenv("STORAGE_TEST_ACCESS_KEY"),
		SecretKey: os.Getenv("STORAGE_TEST_SECRET_KEY"),
		Bucket:    fmt.Sprintf("storage-test-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(ctx); err == nil {
		t.Fatal("Ping before EnsureBucket: want error for a missing bucket")
	}
	for range 2 { // idempotent
		if err := c.EnsureBucket(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	// Unknown size: streamed from a pipe, as an upload would be.
	want := bytes.Repeat([]byte("0123456789abcdef"), 1<<16) // 1 MiB
	pr, pw := io.Pipe()
	go func() {
		_, err := pw.Write(want)
		pw.CloseWithError(err)
	}()
	if err := c.Put(ctx, "videos/test/in.mp4", pr, -1, "video/mp4"); err != nil {
		t.Fatal(err)
	}
	rc, err := c.Get(ctx, "videos/test/in.mp4")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("round trip: got %d bytes, want %d", len(got), len(want))
	}
	if _, err := c.Get(ctx, "videos/test/missing.mp4"); !errors.Is(err, app.ErrObjectNotFound) {
		t.Errorf("missing key: err = %v, want ErrObjectNotFound", err)
	}

	// Clean up the test bucket.
	if err := c.s3.RemoveObject(ctx, c.bucket, "videos/test/in.mp4", minio.RemoveObjectOptions{}); err != nil {
		t.Logf("cleanup: %v", err)
	}
	if err := c.s3.RemoveBucket(ctx, c.bucket); err != nil {
		t.Logf("cleanup: %v", err)
	}
}
