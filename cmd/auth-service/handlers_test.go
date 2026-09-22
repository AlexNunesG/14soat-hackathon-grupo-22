package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/14SOAT-HACKATHON/app/internal/auth"
	"github.com/14SOAT-HACKATHON/app/internal/users"
)

// mockRepository is an in-memory users.Repository used to unit test the
// handlers without a real Postgres instance.
type mockRepository struct {
	mu      sync.Mutex
	byEmail map[string]*users.User
	nextID  int
}

func newMockRepository() *mockRepository {
	return &mockRepository{byEmail: make(map[string]*users.User)}
}

func (m *mockRepository) Create(_ context.Context, email, passwordHash string) (*users.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.byEmail[email]; exists {
		return nil, users.ErrDuplicateEmail
	}

	m.nextID++
	u := &users.User{
		ID:           fmt.Sprintf("00000000-0000-0000-0000-%012d", m.nextID),
		Email:        email,
		PasswordHash: passwordHash,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	m.byEmail[email] = u
	return u, nil
}

func (m *mockRepository) FindByEmail(_ context.Context, email string) (*users.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.byEmail[email]
	if !ok {
		return nil, users.ErrNotFound
	}
	return u, nil
}

var _ users.Repository = (*mockRepository)(nil)

func newTestServer() (*server, *mockRepository) {
	repo := newMockRepository()
	return &server{
		repo:      repo,
		jwtSecret: "test-secret",
		jwtTTL:    time.Hour,
	}, repo
}

func doRequest(t *testing.T, handler http.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestHandleRegister_Success(t *testing.T) {
	s, _ := newTestServer()
	rec := doRequest(t, s.handleRegister, http.MethodPost, `{"email":"user@example.com","password":"password123"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp registerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Email != "user@example.com" {
		t.Errorf("resp.Email = %q, want %q", resp.Email, "user@example.com")
	}
	if resp.ID == "" {
		t.Error("resp.ID is empty")
	}
	if rec.Body.String() != "" && bytesContains(rec.Body.Bytes(), "password") {
		t.Error("register response must never include the password/hash")
	}
}

func TestHandleRegister_InvalidEmail(t *testing.T) {
	s, _ := newTestServer()
	rec := doRequest(t, s.handleRegister, http.MethodPost, `{"email":"not-an-email","password":"password123"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleRegister_ShortPassword(t *testing.T) {
	s, _ := newTestServer()
	rec := doRequest(t, s.handleRegister, http.MethodPost, `{"email":"user@example.com","password":"short"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleRegister_MalformedBody(t *testing.T) {
	s, _ := newTestServer()
	rec := doRequest(t, s.handleRegister, http.MethodPost, `not-json`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleRegister_DuplicateEmail(t *testing.T) {
	s, _ := newTestServer()
	doRequest(t, s.handleRegister, http.MethodPost, `{"email":"user@example.com","password":"password123"}`)
	rec := doRequest(t, s.handleRegister, http.MethodPost, `{"email":"user@example.com","password":"password123"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestHandleLogin_Success(t *testing.T) {
	s, _ := newTestServer()
	doRequest(t, s.handleRegister, http.MethodPost, `{"email":"user@example.com","password":"password123"}`)

	rec := doRequest(t, s.handleLogin, http.MethodPost, `{"email":"user@example.com","password":"password123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("resp.Token is empty")
	}
	if resp.TokenType != "Bearer" {
		t.Errorf("resp.TokenType = %q, want %q", resp.TokenType, "Bearer")
	}

	claims, err := auth.VerifyToken(s.jwtSecret, resp.Token)
	if err != nil {
		t.Fatalf("VerifyToken() error = %v", err)
	}
	if claims.Subject == "" {
		t.Error("claims.Subject is empty")
	}
}

func TestHandleLogin_WrongPassword(t *testing.T) {
	s, _ := newTestServer()
	doRequest(t, s.handleRegister, http.MethodPost, `{"email":"user@example.com","password":"password123"}`)

	rec := doRequest(t, s.handleLogin, http.MethodPost, `{"email":"user@example.com","password":"wrongpass"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleLogin_UnknownEmail(t *testing.T) {
	s, _ := newTestServer()
	rec := doRequest(t, s.handleLogin, http.MethodPost, `{"email":"nobody@example.com","password":"password123"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleLogin_MalformedBody(t *testing.T) {
	s, _ := newTestServer()
	rec := doRequest(t, s.handleLogin, http.MethodPost, `not-json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleHealth(t *testing.T) {
	s, _ := newTestServer()
	rec := doRequest(t, s.handleHealth, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func bytesContains(b []byte, substr string) bool {
	return bytes.Contains(b, []byte(substr))
}
