package mailer

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"video-processor/internal/app"
)

// fakeServer is a scripted SMTP server: it answers every command with 250
// (EHLO with the extensions in ehlo) except those in replies, keyed by
// the command verb ("MAIL", "RCPT", "DATA", "BODY" for the end of data).
type fakeServer struct {
	ln      net.Listener
	ehlo    []string
	replies map[string]string
	// silent makes the server accept connections and never greet.
	silent bool

	mu       sync.Mutex
	messages []string
	commands []string
}

// newFakeServer starts a server configured by setup (nil: defaults).
func newFakeServer(t *testing.T, setup func(*fakeServer)) *fakeServer {
	t.Helper()
	return startFakeServer(t, nil, setup)
}

// startFakeServer is newFakeServer, speaking implicit TLS when tlsCfg is
// set.
func startFakeServer(t *testing.T, tlsCfg *tls.Config, setup func(*fakeServer)) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	s := &fakeServer{ln: ln, replies: map[string]string{}}
	if setup != nil {
		setup(s)
	}
	t.Cleanup(func() { ln.Close() })
	go s.serve()
	return s
}

func (s *fakeServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.session(conn)
	}
}

func (s *fakeServer) session(conn net.Conn) {
	defer conn.Close()
	if s.silent {
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	r := bufio.NewReader(conn)
	reply := func(line string) { fmt.Fprintf(conn, "%s\r\n", line) }
	reply("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.Fields(line + " x")[0])
		s.mu.Lock()
		s.commands = append(s.commands, line)
		s.mu.Unlock()
		if custom, ok := s.replies[verb]; ok && verb != "BODY" {
			reply(custom)
			continue
		}
		switch verb {
		case "EHLO":
			lines := append([]string{"fake"}, s.ehlo...)
			for i, l := range lines {
				sep := "-"
				if i == len(lines)-1 {
					sep = " "
				}
				reply("250" + sep + l)
			}
		case "DATA":
			reply("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			s.mu.Lock()
			s.messages = append(s.messages, b.String())
			s.mu.Unlock()
			if custom, ok := s.replies["BODY"]; ok {
				reply(custom)
			} else {
				reply("250 queued")
			}
		case "AUTH":
			reply("235 2.7.0 authenticated")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func (s *fakeServer) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.messages...)
}

func newMailer(t *testing.T, port int, mutate func(*Config)) *SMTP {
	t.Helper()
	cfg := Config{Host: "127.0.0.1", Port: port, From: "FIAP X <no-reply@fiapx.local>", TLS: TLSNone, Timeout: 5 * time.Second}
	if mutate != nil {
		mutate(&cfg)
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) }
	return m
}

var testMail = app.Mail{
	ID:      "00000000-0000-4000-8000-000000000001",
	To:      "ana@example.com",
	Subject: "Video processing failed: férias corrompidas.mp4",
	Body:    "Hello,\n\nReason: invalid data found when processing input — ação\n.leading dot line\n",
}

// parse reads a raw message and returns its decoded subject and body.
func parse(t *testing.T, raw string) (*mail.Message, string, string) {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("invalid message: %v\n%s", err, raw)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		t.Fatal(err)
	}
	return msg, subject, string(body)
}

func TestSendComposesAMIMEMessage(t *testing.T) {
	srv := newFakeServer(t, nil)
	if err := newMailer(t, srv.port(), nil).Send(context.Background(), testMail); err != nil {
		t.Fatal(err)
	}
	got := srv.received()
	if len(got) != 1 {
		t.Fatalf("%d messages", len(got))
	}
	msg, subject, body := parse(t, got[0])
	if subject != testMail.Subject {
		t.Errorf("subject %q", subject)
	}
	if raw := msg.Header.Get("Subject"); !strings.HasPrefix(raw, "=?utf-8?q?") {
		t.Errorf("raw subject %q is not an encoded-word", raw)
	}
	if body != strings.ReplaceAll(testMail.Body, "\n", "\r\n") {
		t.Errorf("body %q", body)
	}
	for h, want := range map[string]string{
		"From":                      `"FIAP X" <no-reply@fiapx.local>`,
		"To":                        "<ana@example.com>",
		"Content-Type":              "text/plain; charset=utf-8",
		"Content-Transfer-Encoding": "quoted-printable",
		"Message-Id":                "<" + testMail.ID + "@fiapx.local>",
		"Date":                      "Thu, 24 Sep 2026 12:00:00 +0000",
		"Mime-Version":              "1.0",
	} {
		if v := msg.Header.Get(h); v != want {
			t.Errorf("%s: %q, want %q", h, v, want)
		}
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !contains(srv.commands, "MAIL FROM:<no-reply@fiapx.local>") || !contains(srv.commands, "RCPT TO:<ana@example.com>") {
		t.Errorf("commands %q", srv.commands)
	}
}

func contains(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func TestEncodeHeader(t *testing.T) {
	dec := new(mime.WordDecoder)
	for _, v := range []string{
		"plain.mp4",
		"boas férias.mp4",
		strings.Repeat("vídeo longo ", 20) + ".mkv",
		"=?utf-8?q?looks_encoded?=.mp4",
		"line\r\nBcc: attacker@example.com",
	} {
		enc := encodeHeader(v)
		for _, line := range strings.Split(enc, "\r\n") {
			if len(line) > 78 && v != "plain.mp4" {
				t.Errorf("%q: header line of %d characters: %q", v, len(line), line)
			}
		}
		if strings.Contains(strings.ReplaceAll(enc, "\r\n ", ""), "\n") {
			t.Errorf("%q: line break left in %q", v, enc)
		}
		got, err := dec.DecodeHeader(strings.ReplaceAll(enc, "\r\n", ""))
		if err != nil || got != v {
			t.Errorf("%q encoded as %q decodes to %q, %v", v, enc, got, err)
		}
	}
}

func TestSendClassifiesReplies(t *testing.T) {
	for name, tt := range map[string]struct {
		replies   map[string]string
		permanent bool
	}{
		"unknown recipient":  {replies: map[string]string{"RCPT": "550 5.1.1 no such user"}, permanent: true},
		"sender refused":     {replies: map[string]string{"MAIL": "553 5.7.1 sender rejected"}, permanent: true},
		"message rejected":   {replies: map[string]string{"BODY": "554 5.6.0 message refused"}, permanent: true},
		"mailbox busy":       {replies: map[string]string{"RCPT": "450 4.2.1 try later"}},
		"server overloaded":  {replies: map[string]string{"MAIL": "421 4.3.2 shutting down"}},
		"temporary on data":  {replies: map[string]string{"BODY": "451 4.3.0 try again"}},
		"data command fails": {replies: map[string]string{"DATA": "452 4.3.1 insufficient storage"}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newFakeServer(t, func(s *fakeServer) { s.replies = tt.replies })
			err := newMailer(t, srv.port(), nil).Send(context.Background(), testMail)
			if err == nil {
				t.Fatal("no error")
			}
			if got := errors.Is(err, app.ErrPermanent); got != tt.permanent {
				t.Errorf("permanent = %v, want %v (%v)", got, tt.permanent, err)
			}
		})
	}
}

func TestSendTransientConnectionErrors(t *testing.T) {
	// Nothing listens on the port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	err = newMailer(t, port, nil).Send(context.Background(), testMail)
	if err == nil || errors.Is(err, app.ErrPermanent) {
		t.Errorf("connection refused: err = %v, want a transient error", err)
	}

	// A server that never answers: bounded by the timeout.
	srv := newFakeServer(t, func(s *fakeServer) { s.silent = true })
	start := time.Now()
	err = newMailer(t, srv.port(), func(c *Config) { c.Timeout = 200 * time.Millisecond }).Send(context.Background(), testMail)
	if err == nil || errors.Is(err, app.ErrPermanent) || time.Since(start) > 3*time.Second {
		t.Errorf("silent server: err = %v after %s", err, time.Since(start))
	}
}

// testCertificate returns a self-signed certificate for 127.0.0.1 and the
// client configuration that trusts it.
func testCertificate(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	server = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	client = &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	return server, client
}

func TestSendImplicitTLSWithAuth(t *testing.T) {
	serverTLS, clientTLS := testCertificate(t)
	srv := startFakeServer(t, serverTLS, func(s *fakeServer) { s.ehlo = []string{"AUTH PLAIN"} })
	m := newMailer(t, srv.port(), func(c *Config) { c.TLS, c.Username, c.Password = TLSImplicit, "user", "secret" })
	m.tlsConfig = clientTLS
	if err := m.Send(context.Background(), testMail); err != nil {
		t.Fatal(err)
	}
	if len(srv.received()) != 1 {
		t.Fatalf("%d messages", len(srv.received()))
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if !contains(srv.commands, "AUTH PLAIN ") {
		t.Errorf("commands %q, want AUTH PLAIN", srv.commands)
	}

	// An untrusted certificate is refused (transient: the operator fixes it).
	m2 := newMailer(t, srv.port(), func(c *Config) { c.TLS = TLSImplicit })
	if err := m2.Send(context.Background(), testMail); err == nil || errors.Is(err, app.ErrPermanent) {
		t.Errorf("untrusted certificate: err = %v", err)
	}
}

func TestSendRequiresOfferedStartTLS(t *testing.T) {
	srv := newFakeServer(t, nil) // offers no STARTTLS
	err := newMailer(t, srv.port(), func(c *Config) { c.TLS = TLSStartTLS }).Send(context.Background(), testMail)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("err = %v, want STARTTLS not offered", err)
	}
	if len(srv.received()) != 0 {
		t.Error("a message was sent without TLS")
	}
}

func TestSendInvalidRecipientIsPermanent(t *testing.T) {
	srv := newFakeServer(t, nil)
	m := testMail
	m.To = "not an address"
	if err := newMailer(t, srv.port(), nil).Send(context.Background(), m); !errors.Is(err, app.ErrPermanent) {
		t.Errorf("err = %v, want ErrPermanent", err)
	}
}

func TestNewValidates(t *testing.T) {
	ok := Config{Host: "smtp.example.com", Port: 587, From: "a@b.c", TLS: TLSStartTLS, Username: "u", Password: "p"}
	if _, err := New(ok); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"no host":          func(c *Config) { c.Host = "" },
		"bad port":         func(c *Config) { c.Port = 0 },
		"bad sender":       func(c *Config) { c.From = "nope" },
		"bad TLS mode":     func(c *Config) { c.TLS = "ssl" },
		"auth without TLS": func(c *Config) { c.TLS = TLSNone },
	} {
		cfg := ok
		mutate(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestSendThroughMailHog sends a real e-mail through the SMTP server at
// SMTP_TEST_ADDR (e.g. MailHog: `make up`, then
// SMTP_TEST_ADDR=localhost:1025 MAILHOG_TEST_URL=http://localhost:8025).
// With MAILHOG_TEST_URL it also reads the e-mail back from MailHog.
func TestSendThroughMailHog(t *testing.T) {
	addr := os.Getenv("SMTP_TEST_ADDR")
	if addr == "" {
		t.Skip("SMTP_TEST_ADDR not set")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{Host: host, Port: port, From: "FIAP X Video Processor <no-reply@fiapx.local>", TLS: TLSNone})
	if err != nil {
		t.Fatal(err)
	}
	mail := testMail
	mail.ID = uuid.NewString()
	mail.To = "mailer-test-" + mail.ID + "@example.com"
	if err := m.Send(context.Background(), mail); err != nil {
		t.Fatal(err)
	}

	api := os.Getenv("MAILHOG_TEST_URL")
	if api == "" {
		return
	}
	u := api + "/api/v2/search?" + url.Values{"kind": {"to"}, "query": {mail.To}}.Encode()
	resp, err := http.Get(u) // #nosec G107 G704 -- test URL from the environment
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var search struct {
		Items []struct {
			Content struct {
				Headers map[string][]string
				Body    string
			}
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&search); err != nil {
		t.Fatal(err)
	}
	if len(search.Items) != 1 {
		t.Fatalf("%d e-mails to %s in MailHog, want 1", len(search.Items), mail.To)
	}
	item := search.Items[0].Content
	subject, err := new(mime.WordDecoder).DecodeHeader(strings.Join(item.Headers["Subject"], ""))
	if err != nil || subject != mail.Subject {
		t.Errorf("subject %q, %v", subject, err)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(item.Body)))
	if err != nil || !strings.Contains(string(body), "ação") {
		t.Errorf("body %q, %v", body, err)
	}
}
