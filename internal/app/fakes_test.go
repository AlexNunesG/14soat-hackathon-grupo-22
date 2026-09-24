package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	"video-processor/internal/app"
)

// fakeStorage is an in-memory app.ObjectStorage.
type fakeStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
	types   map[string]string
	putErr  map[string]error // by key; "*" for every key
	getErr  error
	deleted []string
	puts    int
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: map[string][]byte{}, types: map[string]string{}, putErr: map[string]error{}}
}

func (s *fakeStorage) Put(_ context.Context, key string, r io.Reader, size int64, contentType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	if err := s.putErr[key]; err != nil {
		return err
	}
	if err := s.putErr["*"]; err != nil {
		return err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if size >= 0 && int64(len(data)) != size {
		return fmt.Errorf("put %s: read %d bytes, size says %d", key, len(data), size)
	}
	s.objects[key], s.types[key] = data, contentType
	return nil
}

func (s *fakeStorage) Get(_ context.Context, key string) (*app.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	data, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("get %s: %w", key, app.ErrObjectNotFound)
	}
	return &app.Object{ReadCloser: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data))}, nil
}

func (s *fakeStorage) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	s.deleted = append(s.deleted, key)
	return nil
}

func (*fakeStorage) EnsureBucket(context.Context) error { return nil }
func (*fakeStorage) Ping(context.Context) error         { return nil }

// keys returns the stored keys, sorted.
func (s *fakeStorage) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var errBoom = errors.New("boom")
