package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"video-processor/internal/app"
	"video-processor/internal/domain"
)

// AuthService is what the auth handlers need from the app (app.Auth).
type AuthService interface {
	Register(ctx context.Context, in app.RegisterInput) (*domain.User, error)
	Login(ctx context.Context, email, password string) (app.AccessToken, error)
}

// maxJSONBody bounds the JSON request bodies (register and login).
const maxJSONBody = 1 << 20

// userIDKey is the gin context key holding the authenticated user's id.
const userIDKey = "auth.userID"

type registerRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// userBody is the User schema: never the password or its hash.
type userBody struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
}

// tokenBody is the Token schema.
type tokenBody struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

// formatTime renders a timestamp as RFC 3339 in UTC, keeping the stored
// sub-second precision so equal instants always render the same.
func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// register handles POST /api/v1/auth/register.
func register(log *slog.Logger, auth AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req registerRequest
		if !decodeJSON(c, &req) {
			return
		}
		u, err := auth.Register(c.Request.Context(), app.RegisterInput{
			Name: req.Name, Email: req.Email, Password: req.Password,
		})
		switch {
		case err == nil:
			c.JSON(http.StatusCreated, userBody{
				ID: u.ID, Name: u.Name, Email: u.Email, CreatedAt: formatTime(u.CreatedAt),
			})
		case errors.Is(err, app.ErrEmailTaken):
			WriteError(c, http.StatusConflict, CodeEmailTaken, "e-mail is already registered")
		default:
			writeAppError(c, log, err)
		}
	}
}

// login handles POST /api/v1/auth/login.
func login(log *slog.Logger, auth AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req loginRequest
		if !decodeJSON(c, &req) {
			return
		}
		tok, err := auth.Login(c.Request.Context(), req.Email, req.Password)
		switch {
		case err == nil:
			// RFC 6749 §5.1: token responses must not be cached.
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusOK, tokenBody{
				AccessToken: tok.Value,
				TokenType:   "Bearer",
				ExpiresIn:   int64(tok.ExpiresIn / time.Second),
			})
		case errors.Is(err, app.ErrInvalidCredentials):
			WriteError(c, http.StatusUnauthorized, CodeInvalidCredentials, "invalid e-mail or password")
		default:
			writeAppError(c, log, err)
		}
	}
}

// decodeJSON decodes the request body, a single JSON object of at most
// maxJSONBody bytes, into dst. On failure it answers 400 invalid_request
// and returns false.
func decodeJSON(c *gin.Context, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBody))
	err := dec.Decode(dst)
	if err == nil && !errors.Is(dec.Decode(&struct{}{}), io.EOF) {
		err = errors.New("trailing data after the JSON object")
	}
	if err == nil {
		return true
	}
	msg := "request body must be a JSON object"
	var tooLarge *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &tooLarge):
		msg = "request body is too large"
	case errors.As(err, &typeErr) && typeErr.Field != "":
		msg = typeErr.Field + ": must be a " + typeErr.Type.String()
	}
	WriteError(c, http.StatusBadRequest, CodeInvalidRequest, msg)
	return false
}

// writeAppError answers the errors every use case may return: invalid
// input (400) and anything unexpected (500, logged, details hidden).
func writeAppError(c *gin.Context, log *slog.Logger, err error) {
	var verr *app.ValidationError
	if errors.As(err, &verr) {
		WriteError(c, http.StatusBadRequest, CodeInvalidRequest, verr.Error())
		return
	}
	log.ErrorContext(c.Request.Context(), "request failed",
		slog.String("path", c.Request.URL.Path), slog.Any("error", err))
	WriteError(c, http.StatusInternalServerError, CodeInternal, "internal server error")
}

// requireAuth rejects requests without a valid bearer token with 401
// unauthorized, and stores the token's user id for the handlers.
func requireAuth(log *slog.Logger, tokens app.TokenVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			unauthorized(c)
			return
		}
		userID, err := tokens.Verify(token)
		if err != nil {
			log.DebugContext(c.Request.Context(), "rejected bearer token", slog.Any("error", err))
			unauthorized(c)
			return
		}
		c.Set(userIDKey, userID)
		c.Next()
	}
}

// bearerToken extracts the token of an "Authorization: Bearer <token>"
// header (the scheme is case-insensitive, RFC 7235).
func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

func unauthorized(c *gin.Context) {
	c.Header("WWW-Authenticate", "Bearer")
	WriteError(c, http.StatusUnauthorized, CodeUnauthorized, "missing or invalid bearer token")
}

// currentUserID returns the id stored by requireAuth.
func currentUserID(c *gin.Context) string { return c.GetString(userIDKey) }
