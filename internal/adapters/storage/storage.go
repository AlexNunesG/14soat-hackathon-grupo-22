// Package storage implements app.ObjectStorage on any S3-compatible object
// store (SeaweedFS locally, MinIO or AWS S3 elsewhere) with the minio-go
// client. It speaks plain S3 with path-style bucket addressing, so the
// backend is swappable by configuration only.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"video-processor/internal/app"
)

// DefaultRegion is used when Config.Region is empty. Setting a region
// spares the client a GetBucketLocation round trip.
const DefaultRegion = "us-east-1"

// Config locates the object store and the bucket.
type Config struct {
	// Endpoint is host:port, or a URL whose scheme (http/https) sets UseSSL.
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

// Client is an app.ObjectStorage bound to one bucket.
type Client struct {
	s3     *minio.Client
	bucket string
	region string
}

var _ app.ObjectStorage = (*Client)(nil)

// New returns a Client for cfg. It does not contact the server.
func New(cfg Config) (*Client, error) {
	endpoint, secure, err := parseEndpoint(cfg.Endpoint, cfg.UseSSL)
	if err != nil {
		return nil, err
	}
	if cfg.Bucket == "" {
		return nil, errors.New("storage: bucket is empty")
	}
	region := cfg.Region
	if region == "" {
		region = DefaultRegion
	}
	s3, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       secure,
		Region:       region,
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	return &Client{s3: s3, bucket: cfg.Bucket, region: region}, nil
}

// parseEndpoint accepts "host:port" or "http(s)://host:port" and returns
// host:port and whether to use TLS.
func parseEndpoint(endpoint string, useSSL bool) (string, bool, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", false, errors.New("storage: endpoint is empty")
	}
	if !strings.Contains(endpoint, "://") {
		return strings.TrimRight(endpoint, "/"), useSSL, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", false, fmt.Errorf("storage: invalid endpoint %q: %w", endpoint, err)
	}
	if u.Host == "" || (u.Path != "" && u.Path != "/") {
		return "", false, fmt.Errorf("storage: invalid endpoint %q: want scheme://host[:port]", endpoint)
	}
	switch u.Scheme {
	case "http":
		return u.Host, false, nil
	case "https":
		return u.Host, true, nil
	default:
		return "", false, fmt.Errorf("storage: invalid endpoint %q: scheme must be http or https", endpoint)
	}
}

// Bucket returns the bucket name.
func (c *Client) Bucket() string { return c.bucket }

// Put streams r to key. With size -1 the object is uploaded in parts, so
// the length need not be known in advance.
func (c *Client) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if _, err := c.s3.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return fmt.Errorf("storage: put %s: %w", key, err)
	}
	return nil
}

// Get opens the object at key. A missing key yields an error wrapping
// app.ErrObjectNotFound.
func (c *Client) Get(ctx context.Context, key string) (*app.Object, error) {
	obj, err := c.s3.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, c.wrap("get", key, err)
	}
	// GetObject is lazy: Stat makes the request, so a missing key fails
	// here instead of on the caller's first Read.
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, c.wrap("get", key, err)
	}
	return &app.Object{ReadCloser: obj, Size: info.Size}, nil
}

// Delete removes the object at key. S3 reports success for a missing key.
func (c *Client) Delete(ctx context.Context, key string) error {
	if err := c.s3.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return c.wrap("delete", key, err)
	}
	return nil
}

// EnsureBucket creates the bucket when it does not exist. Concurrent
// callers are fine: "already exists" counts as success.
func (c *Client) EnsureBucket(ctx context.Context) error {
	exists, err := c.s3.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("storage: checking bucket %s: %w", c.bucket, err)
	}
	if exists {
		return nil
	}
	if err := c.s3.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{Region: c.region}); err != nil {
		switch minio.ToErrorResponse(err).Code {
		case "BucketAlreadyOwnedByYou", "BucketAlreadyExists":
			return nil
		}
		return fmt.Errorf("storage: creating bucket %s: %w", c.bucket, err)
	}
	return nil
}

// Ping checks that the store answers and the bucket exists.
func (c *Client) Ping(ctx context.Context) error {
	exists, err := c.s3.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	if !exists {
		return fmt.Errorf("storage: bucket %s does not exist", c.bucket)
	}
	return nil
}

func (c *Client) wrap(op, key string, err error) error {
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NotFound":
		return fmt.Errorf("storage: %s %s: %w", op, key, app.ErrObjectNotFound)
	}
	return fmt.Errorf("storage: %s %s: %w", op, key, err)
}
