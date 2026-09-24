package app

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"video-processor/internal/domain"
)

// Limits of the registration fields (docs/openapi.yaml, RegisterRequest).
const (
	MaxNameLength     = 100 // characters, after trimming
	MinPasswordLength = 8   // characters
	// MaxPasswordBytes is bcrypt's input limit: longer passwords would be
	// silently truncated, so they are rejected.
	MaxPasswordBytes = 72
	// maxEmailLength is the practical limit of an address (RFC 5321 path).
	maxEmailLength = 254
)

// Auth holds the sign-up and login use cases (RF3).
type Auth struct {
	users  UserRepository
	hasher PasswordHasher
	tokens TokenIssuer
	now    func() time.Time
	newID  func() string

	// dummyHash is compared against when the e-mail is unknown, so Login
	// costs about the same whether or not the user exists.
	dummyHash func() (string, error)
}

// AuthOption customizes Auth (tests use it to fix the clock and ids).
type AuthOption func(*Auth)

// WithClock sets the clock used for timestamps.
func WithClock(now func() time.Time) AuthOption { return func(a *Auth) { a.now = now } }

// WithIDGenerator sets the generator of user ids.
func WithIDGenerator(newID func() string) AuthOption { return func(a *Auth) { a.newID = newID } }

// NewAuth returns the auth use cases.
func NewAuth(users UserRepository, hasher PasswordHasher, tokens TokenIssuer, opts ...AuthOption) *Auth {
	a := &Auth{users: users, hasher: hasher, tokens: tokens, now: time.Now, newID: uuid.NewString}
	for _, opt := range opts {
		opt(a)
	}
	a.dummyHash = sync.OnceValues(func() (string, error) {
		return hasher.Hash("dummy password for unknown e-mails")
	})
	return a
}

// RegisterInput is the sign-up request.
type RegisterInput struct {
	Name     string
	Email    string
	Password string
}

// Register validates the input and creates a user with the password
// hashed and the e-mail normalized. It fails with a *ValidationError for
// invalid input and with an error wrapping ErrEmailTaken when the e-mail is
// already registered.
func (a *Auth) Register(ctx context.Context, in RegisterInput) (*domain.User, error) {
	name := strings.TrimSpace(in.Name)
	email, err := validateRegistration(name, in.Email, in.Password)
	if err != nil {
		return nil, err
	}
	hash, err := a.hasher.Hash(in.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	u := &domain.User{
		ID:           a.newID(),
		Name:         name,
		Email:        email,
		PasswordHash: hash,
		// The database keeps microseconds: truncate so the returned user
		// matches what is stored.
		CreatedAt: a.now().UTC().Truncate(time.Microsecond),
	}
	if err := a.users.Create(ctx, u); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

// validateRegistration checks the registration fields (name already
// trimmed) and returns the normalized e-mail.
func validateRegistration(name, email, password string) (string, error) {
	if n := utf8.RuneCountInString(name); n < 1 || n > MaxNameLength {
		return "", invalid("name", "must have between 1 and %d characters", MaxNameLength)
	}
	normalized, err := normalizeEmail(email)
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return "", invalid("password", "must have at least %d characters", MinPasswordLength)
	}
	if len(password) > MaxPasswordBytes {
		return "", invalid("password", "must have at most %d bytes", MaxPasswordBytes)
	}
	return normalized, nil
}

// normalizeEmail validates a bare address (no display name, e.g.
// "ada@example.com") and returns it normalized.
func normalizeEmail(email string) (string, error) {
	email = domain.NormalizeEmail(email)
	if email == "" {
		return "", invalid("email", "is required")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email || len(email) > maxEmailLength ||
		!strings.Contains(email[strings.LastIndexByte(email, '@')+1:], ".") {
		return "", invalid("email", "must be a valid e-mail address")
	}
	return email, nil
}

// Login checks the credentials and issues an access token. An unknown
// e-mail and a wrong password both yield ErrInvalidCredentials; an empty
// e-mail or password yields a *ValidationError.
func (a *Auth) Login(ctx context.Context, email, password string) (AccessToken, error) {
	email = domain.NormalizeEmail(email)
	if email == "" {
		return AccessToken{}, invalid("email", "is required")
	}
	if password == "" {
		return AccessToken{}, invalid("password", "is required")
	}
	u, err := a.users.GetByEmail(ctx, email)
	switch {
	case errors.Is(err, ErrNotFound):
		// Spend the same time as a real check, so response times do not
		// tell registered e-mails apart.
		if hash, herr := a.dummyHash(); herr == nil {
			_ = a.hasher.Compare(hash, password)
		}
		return AccessToken{}, ErrInvalidCredentials
	case err != nil:
		return AccessToken{}, fmt.Errorf("find user: %w", err)
	}
	if err := a.hasher.Compare(u.PasswordHash, password); err != nil {
		return AccessToken{}, ErrInvalidCredentials
	}
	tok, err := a.tokens.Issue(u.ID)
	if err != nil {
		return AccessToken{}, fmt.Errorf("issue token: %w", err)
	}
	return tok, nil
}
