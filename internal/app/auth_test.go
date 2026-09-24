package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// fakeUsers is an in-memory app.UserRepository.
type fakeUsers struct {
	mu      sync.Mutex
	byEmail map[string]domain.User
	err     error // returned by every call when set
}

func newFakeUsers() *fakeUsers { return &fakeUsers{byEmail: map[string]domain.User{}} }

func (f *fakeUsers) Create(_ context.Context, u *domain.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if _, ok := f.byEmail[u.Email]; ok {
		return app.ErrEmailTaken
	}
	f.byEmail[u.Email] = *u
	return nil
}

func (f *fakeUsers) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	u, ok := f.byEmail[email]
	if !ok {
		return nil, app.ErrNotFound
	}
	return &u, nil
}

func (f *fakeUsers) GetByID(_ context.Context, id string) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.byEmail {
		if u.ID == id {
			return &u, nil
		}
	}
	return nil, app.ErrNotFound
}

// fakeHasher "hashes" by prefixing, and counts comparisons.
type fakeHasher struct {
	mu       sync.Mutex
	compared []string // hashes passed to Compare
}

func (*fakeHasher) Hash(password string) (string, error) { return "hash:" + password, nil }

func (h *fakeHasher) Compare(hash, password string) error {
	h.mu.Lock()
	h.compared = append(h.compared, hash)
	h.mu.Unlock()
	if hash != "hash:"+password {
		return errors.New("mismatch")
	}
	return nil
}

// fakeIssuer issues "token-for-<id>".
type fakeIssuer struct{ err error }

func (f fakeIssuer) Issue(userID string) (app.AccessToken, error) {
	if f.err != nil {
		return app.AccessToken{}, f.err
	}
	return app.AccessToken{Value: "token-for-" + userID, ExpiresIn: time.Hour}, nil
}

var fixedNow = time.Date(2026, 9, 24, 12, 0, 0, 123456789, time.FixedZone("BRT", -3*3600))

func newAuth(users *fakeUsers, hasher *fakeHasher) *app.Auth {
	return app.NewAuth(users, hasher, fakeIssuer{},
		app.WithClock(func() time.Time { return fixedNow }),
		app.WithIDGenerator(func() string { return "0b8f6a2e-4c1d-4f7a-9e3b-2d5c8a1f6e90" }),
	)
}

func TestRegisterCreatesUser(t *testing.T) {
	users := newFakeUsers()
	a := newAuth(users, &fakeHasher{})

	u, err := a.Register(context.Background(), app.RegisterInput{
		Name: "  Ada Lovelace ", Email: " Ada@Example.COM", Password: "correct-horse",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := domain.User{
		ID:           "0b8f6a2e-4c1d-4f7a-9e3b-2d5c8a1f6e90",
		Name:         "Ada Lovelace",
		Email:        "ada@example.com",
		PasswordHash: "hash:correct-horse",
		CreatedAt:    time.Date(2026, 9, 24, 15, 0, 0, 123456000, time.UTC),
	}
	if *u != want {
		t.Errorf("user\n got %+v\nwant %+v", *u, want)
	}
	if stored, _ := users.GetByEmail(context.Background(), "ada@example.com"); stored == nil || *stored != want {
		t.Errorf("stored %+v, want %+v", stored, want)
	}
}

func TestRegisterValidatesInput(t *testing.T) {
	valid := app.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "long-enough"}
	tests := []struct {
		name  string
		edit  func(*app.RegisterInput)
		field string
	}{
		{"empty name", func(in *app.RegisterInput) { in.Name = "" }, "name"},
		{"blank name", func(in *app.RegisterInput) { in.Name = "   " }, "name"},
		{"name of 101 characters", func(in *app.RegisterInput) { in.Name = strings.Repeat("a", 101) }, "name"},
		{"empty email", func(in *app.RegisterInput) { in.Email = "" }, "email"},
		{"email without @", func(in *app.RegisterInput) { in.Email = "not-an-email" }, "email"},
		{"email with display name", func(in *app.RegisterInput) { in.Email = "Ada <ada@example.com>" }, "email"},
		{"email without domain dot", func(in *app.RegisterInput) { in.Email = "ada@localhost" }, "email"},
		{"email with two addresses", func(in *app.RegisterInput) { in.Email = "a@example.com, b@example.com" }, "email"},
		{"empty password", func(in *app.RegisterInput) { in.Password = "" }, "password"},
		{"password of 7 characters", func(in *app.RegisterInput) { in.Password = "1234567" }, "password"},
		{"password over 72 bytes", func(in *app.RegisterInput) { in.Password = strings.Repeat("p", 73) }, "password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := newFakeUsers()
			in := valid
			tt.edit(&in)
			_, err := newAuth(users, &fakeHasher{}).Register(context.Background(), in)
			var verr *app.ValidationError
			if !errors.As(err, &verr) || !errors.Is(err, app.ErrInvalidInput) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			if verr.Field != tt.field || verr.Message == "" {
				t.Errorf("field %q message %q, want field %q", verr.Field, verr.Message, tt.field)
			}
			if len(users.byEmail) != 0 {
				t.Error("an invalid registration created a user")
			}
		})
	}
}

func TestRegisterAcceptsBoundaryValues(t *testing.T) {
	in := app.RegisterInput{
		Name:     strings.Repeat("é", 100), // 100 characters, 200 bytes
		Email:    "a.b+c@sub.example.org",
		Password: strings.Repeat("p", 8),
	}
	if _, err := newAuth(newFakeUsers(), &fakeHasher{}).Register(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	a := newAuth(newFakeUsers(), &fakeHasher{})
	in := app.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "long-enough"}
	if _, err := a.Register(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	in.Email = "ADA@example.com"
	if _, err := a.Register(context.Background(), in); !errors.Is(err, app.ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestRegisterRepositoryError(t *testing.T) {
	users := newFakeUsers()
	users.err = errors.New("connection refused")
	_, err := newAuth(users, &fakeHasher{}).Register(context.Background(),
		app.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "long-enough"})
	if err == nil || errors.Is(err, app.ErrInvalidInput) || errors.Is(err, app.ErrEmailTaken) {
		t.Fatalf("err = %v, want the repository error", err)
	}
}

func registered(t *testing.T) (*app.Auth, *fakeUsers, *fakeHasher) {
	t.Helper()
	users, hasher := newFakeUsers(), &fakeHasher{}
	a := newAuth(users, hasher)
	if _, err := a.Register(context.Background(),
		app.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "correct-horse"}); err != nil {
		t.Fatal(err)
	}
	return a, users, hasher
}

func TestLoginIssuesToken(t *testing.T) {
	a, _, _ := registered(t)
	for _, email := range []string{"ada@example.com", " ADA@Example.com "} {
		tok, err := a.Login(context.Background(), email, "correct-horse")
		if err != nil {
			t.Fatalf("%s: %v", email, err)
		}
		if tok.Value != "token-for-0b8f6a2e-4c1d-4f7a-9e3b-2d5c8a1f6e90" || tok.ExpiresIn != time.Hour {
			t.Errorf("%s: token %+v", email, tok)
		}
	}
}

func TestLoginInvalidCredentials(t *testing.T) {
	a, _, hasher := registered(t)

	_, wrongPass := a.Login(context.Background(), "ada@example.com", "wrong-password")
	_, unknown := a.Login(context.Background(), "nobody@example.com", "correct-horse")

	for name, err := range map[string]error{"wrong password": wrongPass, "unknown e-mail": unknown} {
		if !errors.Is(err, app.ErrInvalidCredentials) {
			t.Errorf("%s: err = %v, want ErrInvalidCredentials", name, err)
		}
	}
	if wrongPass.Error() != unknown.Error() {
		t.Errorf("errors differ: %q vs %q", wrongPass, unknown)
	}
	// The unknown e-mail still ran a hash comparison (against a dummy
	// hash), so both cases cost about the same time.
	if len(hasher.compared) != 2 || hasher.compared[1] == "hash:correct-horse" {
		t.Errorf("comparisons %q, want one against the user's hash and one against a dummy hash", hasher.compared)
	}
}

func TestLoginRequiresFields(t *testing.T) {
	a, _, _ := registered(t)
	for _, tc := range []struct{ email, password, field string }{
		{"", "correct-horse", "email"},
		{"   ", "correct-horse", "email"},
		{"ada@example.com", "", "password"},
	} {
		_, err := a.Login(context.Background(), tc.email, tc.password)
		var verr *app.ValidationError
		if !errors.As(err, &verr) || verr.Field != tc.field {
			t.Errorf("Login(%q, %q) = %v, want a ValidationError on %s", tc.email, tc.password, err, tc.field)
		}
	}
}

func TestLoginPropagatesUnexpectedErrors(t *testing.T) {
	a, users, _ := registered(t)
	users.err = errors.New("connection refused")
	if _, err := a.Login(context.Background(), "ada@example.com", "correct-horse"); err == nil ||
		errors.Is(err, app.ErrInvalidCredentials) {
		t.Errorf("repository failure: err = %v, want it propagated", err)
	}

	users.err = nil
	failing := app.NewAuth(users, &fakeHasher{}, fakeIssuer{err: errors.New("sign failed")})
	if _, err := failing.Login(context.Background(), "ada@example.com", "correct-horse"); err == nil ||
		errors.Is(err, app.ErrInvalidCredentials) {
		t.Errorf("issuer failure: err = %v, want it propagated", err)
	}
}
