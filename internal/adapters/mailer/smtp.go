// Package mailer is the SMTP adapter of app.Mailer: it sends plain-text
// UTF-8 e-mails through any SMTP server (MailHog locally, a provider's
// relay in production), with optional STARTTLS or implicit TLS and PLAIN
// authentication.
package mailer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"video-processor/internal/app"
	"video-processor/internal/platform/logging"
)

// TLS modes of Config.TLS.
const (
	// TLSNone speaks plain SMTP (MailHog, a relay on a private network).
	// Authentication is refused in this mode.
	TLSNone = "none"
	// TLSStartTLS upgrades the connection with STARTTLS and fails if the
	// server does not offer it (submission port 587).
	TLSStartTLS = "starttls"
	// TLSImplicit connects with TLS from the start (SMTPS, port 465).
	TLSImplicit = "tls"
)

// DefaultTimeout bounds one Send when Config.Timeout is zero.
const DefaultTimeout = 30 * time.Second

// Config configures the SMTP client.
type Config struct {
	Host string
	Port int
	// Username and Password enable PLAIN authentication when Username is
	// set; it needs a TLS mode other than TLSNone.
	Username string
	Password string
	// From is the sender, an RFC 5322 address ("Name <addr@host>" or
	// "addr@host").
	From string
	// TLS is TLSNone, TLSStartTLS or TLSImplicit.
	TLS string
	// Timeout bounds one Send (connect, SMTP commands and data).
	Timeout time.Duration
	// HelloName is the name sent with EHLO ("" is "localhost").
	HelloName string
}

// SMTP is an app.Mailer that opens one SMTP connection per e-mail.
type SMTP struct {
	cfg  Config
	from *mail.Address
	// tlsConfig is used by STARTTLS and implicit TLS; tests replace it.
	tlsConfig *tls.Config
	now       func() time.Time
}

var _ app.Mailer = (*SMTP)(nil)

// New validates cfg and returns the mailer. It does not contact the server.
func New(cfg Config) (*SMTP, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("mailer: host is required")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("mailer: invalid port %d", cfg.Port)
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("mailer: invalid sender %q: %w", cfg.From, err)
	}
	switch cfg.TLS {
	case TLSNone:
		if cfg.Username != "" {
			return nil, errors.New("mailer: authentication needs TLS (starttls or tls), refusing to send credentials in clear text")
		}
	case TLSStartTLS, TLSImplicit:
	default:
		return nil, fmt.Errorf("mailer: TLS mode %q, want %s, %s or %s", cfg.TLS, TLSNone, TLSStartTLS, TLSImplicit)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.HelloName == "" {
		cfg.HelloName = "localhost"
	}
	return &SMTP{
		cfg:       cfg,
		from:      from,
		tlsConfig: &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12},
		now:       time.Now,
	}, nil
}

// Send delivers m. Replies 5xx to MAIL FROM, RCPT TO or DATA (e.g. an
// unknown recipient) are permanent and wrap app.ErrPermanent; connection
// failures, 4xx replies and every other error are worth retrying.
func (s *SMTP) Send(ctx context.Context, m app.Mail) error {
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		// The error is logged: mask the address (docs/observability.md).
		return fmt.Errorf("mailer: invalid recipient %q: %w: %w", logging.MaskEmail(m.To), app.ErrPermanent, err)
	}
	msg, err := s.compose(m, to)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	c, err := s.dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	// Closing the connection interrupts a blocked command when ctx ends.
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()

	if err := s.transact(c, to.Address, msg); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("mailer: send: %w (%w)", ctx.Err(), err)
		}
		return err
	}
	return nil
}

// dial connects (with TLS from the start in TLSImplicit mode), says EHLO
// and, in TLSStartTLS mode, upgrades the connection.
func (s *SMTP) dial(ctx context.Context) (*smtp.Client, error) {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("mailer: connect to %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if s.cfg.TLS == TLSImplicit {
		tc := tls.Client(conn, s.tlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("mailer: TLS handshake with %s: %w", addr, err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("mailer: greeting from %s: %w", addr, err)
	}
	if err := c.Hello(s.cfg.HelloName); err != nil {
		c.Close()
		return nil, fmt.Errorf("mailer: EHLO: %w", err)
	}
	if s.cfg.TLS == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			c.Close()
			return nil, fmt.Errorf("mailer: %s does not offer STARTTLS (SMTP_TLS=starttls)", addr)
		}
		if err := c.StartTLS(s.tlsConfig); err != nil {
			c.Close()
			return nil, fmt.Errorf("mailer: STARTTLS: %w", err)
		}
	}
	return c, nil
}

// transact authenticates if configured and sends one message.
func (s *SMTP) transact(c *smtp.Client, to string, msg []byte) error {
	if s.cfg.Username != "" {
		// Credentials rejected are a configuration problem, not a property
		// of the message: retried like any transient error.
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("mailer: AUTH: %w", err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return classify("MAIL FROM", err)
	}
	if err := c.Rcpt(to); err != nil {
		return classify("RCPT TO", err)
	}
	w, err := c.Data()
	if err != nil {
		return classify("DATA", err)
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("mailer: write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return classify("end of DATA", err)
	}
	// The server accepted the message: a failed QUIT does not matter.
	_ = c.Quit()
	return nil
}

// classify wraps an SMTP error of the command cmd; 5xx replies are
// permanent.
func classify(cmd string, err error) error {
	var tp *textproto.Error
	if errors.As(err, &tp) && tp.Code >= 500 && tp.Code < 600 {
		return fmt.Errorf("mailer: %s refused: %w: %w", cmd, app.ErrPermanent, err)
	}
	return fmt.Errorf("mailer: %s: %w", cmd, err)
}

// compose builds the RFC 5322 message: UTF-8 text/plain body in
// quoted-printable, the subject as RFC 2047 encoded-words when it is not
// plain ASCII, and a Message-ID derived from m.ID.
func (s *SMTP) compose(m app.Mail, to *mail.Address) ([]byte, error) {
	var b bytes.Buffer
	header := func(name, value string) {
		b.WriteString(name + ": " + value + "\r\n")
	}
	header("From", s.from.String())
	header("To", to.String())
	header("Subject", encodeHeader(m.Subject))
	header("Date", s.now().Format(time.RFC1123Z))
	if m.ID != "" {
		header("Message-ID", "<"+m.ID+"@"+domainOf(s.from.Address)+">")
	}
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	body := strings.ReplaceAll(m.Body, "\r\n", "\n")
	if _, err := qp.Write([]byte(body)); err != nil {
		return nil, fmt.Errorf("mailer: encode body: %w", err)
	}
	if err := qp.Close(); err != nil {
		return nil, fmt.Errorf("mailer: encode body: %w", err)
	}
	return b.Bytes(), nil
}

// encodeHeader returns value as an unstructured header value (RFC 2047):
// unchanged when it is printable ASCII, else as UTF-8 Q-encoded words,
// folded one word per line. Encoding also neutralizes line breaks. An
// ASCII value that contains "=?" is B-encoded, so no reader mistakes part
// of it (e.g. a file name) for an encoded-word.
func encodeHeader(value string) string {
	enc := mime.QEncoding.Encode("utf-8", value)
	if enc == value && strings.Contains(value, "=?") {
		enc = bEncode(value)
	}
	return strings.ReplaceAll(enc, "?= =?", "?=\r\n =?")
}

// bEncode encodes an ASCII string as B-encoded words of at most 75
// characters, separated by spaces.
func bEncode(value string) string {
	const chunk = 45 // 60 base64 characters + 12 of "=?utf-8?b?" "?="
	var words []string
	for len(value) > 0 {
		n := min(chunk, len(value))
		words = append(words, "=?utf-8?b?"+base64.StdEncoding.EncodeToString([]byte(value[:n]))+"?=")
		value = value[n:]
	}
	return strings.Join(words, " ")
}

// domainOf returns the domain of an e-mail address.
func domainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 && i+1 < len(addr) {
		return addr[i+1:]
	}
	return "localhost"
}
