package integration

// Helpers that read the e-mails sent by the system from the MailHog HTTP API
// (v2) at MAILHOG_URL. Every test sends mail to its own unique users, so a
// test only ever looks at the mailboxes of the users it registered: other
// tests produce failure e-mails too.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// mailhogMessage is the part of a MailHog v2 message the tests read.
type mailhogMessage struct {
	Content struct {
		Headers map[string][]string `json:"Headers"`
		Body    string              `json:"Body"`
	} `json:"Content"`
	Raw struct {
		To []string `json:"To"`
	} `json:"Raw"`
}

// mailhogSearch is the response of GET /api/v2/search.
type mailhogSearch struct {
	Total int              `json:"total"`
	Items []mailhogMessage `json:"items"`
}

// sentMail is an e-mail with its headers and body decoded to plain text.
type sentMail struct {
	// To holds the addresses of the To header, lowercased.
	To []string
	// Envelope holds the SMTP recipients, lowercased.
	Envelope []string
	Subject  string
	// Body is the decoded text of the message: every text part of a
	// multipart message, with HTML entities unescaped.
	Body string
}

// mailTimeout bounds waitForMails: MAIL_TIMEOUT (a Go duration) or 60s.
func mailTimeout(t *testing.T) time.Duration {
	t.Helper()
	v := os.Getenv("MAIL_TIMEOUT")
	if v == "" {
		return 60 * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("invalid MAIL_TIMEOUT %q: %v", v, err)
	}
	return d
}

// mailsFor returns the e-mails in MailHog addressed to email (in the To
// header or the SMTP envelope), decoded.
func mailsFor(t *testing.T, email string) []sentMail {
	t.Helper()
	email = strings.ToLower(email)
	query := url.Values{"kind": {"to"}, "query": {email}, "limit": {"250"}}
	u := mailhogURL + "/api/v2/search?" + query.Encode()
	resp, err := httpClient.Get(u)
	if err != nil {
		t.Fatalf("MailHog at MAILHOG_URL=%s is not reachable: %v", mailhogURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: reading body: %v", u, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d: %s", u, resp.StatusCode, body)
	}
	var search mailhogSearch
	if err := json.Unmarshal(body, &search); err != nil {
		t.Fatalf("GET %s: invalid JSON: %v (%s)", u, err, body)
	}
	var mails []sentMail
	for _, item := range search.Items {
		m := decodeMail(t, item)
		// The search matches substrings; keep only this exact mailbox.
		for _, addr := range append(m.To, m.Envelope...) {
			if addr == email {
				mails = append(mails, m)
				break
			}
		}
	}
	return mails
}

// waitForMails polls MailHog every 500ms until email has at least n
// e-mails, and returns them. It fails the test after mailTimeout.
func waitForMails(t *testing.T, email string, n int) []sentMail {
	t.Helper()
	timeout := mailTimeout(t)
	deadline := time.Now().Add(timeout)
	for {
		mails := mailsFor(t, email)
		if len(mails) >= n {
			return mails
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s received %d e-mails within %s, want at least %d", email, len(mails), timeout, n)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// header returns the values of the header name, whatever its case.
func header(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			return strings.Join(values, ", ")
		}
	}
	return ""
}

// decodeMail decodes the MIME encoded-word headers and the body of m.
func decodeMail(t *testing.T, m mailhogMessage) sentMail {
	t.Helper()
	h := m.Content.Headers
	dec := &mime.WordDecoder{}
	subject, err := dec.DecodeHeader(unfold(header(h, "Subject")))
	if err != nil {
		t.Fatalf("e-mail subject %q is not valid MIME: %v", header(h, "Subject"), err)
	}
	var out sentMail
	out.Subject = subject
	if to := unfold(header(h, "To")); to != "" {
		addrs, err := (&mail.AddressParser{WordDecoder: dec}).ParseList(to)
		if err != nil {
			t.Fatalf("e-mail To header %q is not a valid address list: %v", to, err)
		}
		for _, a := range addrs {
			out.To = append(out.To, strings.ToLower(a.Address))
		}
	}
	for _, rcpt := range m.Raw.To {
		out.Envelope = append(out.Envelope, strings.ToLower(strings.Trim(rcpt, "<> ")))
	}
	body, err := decodeBody(header(h, "Content-Type"), header(h, "Content-Transfer-Encoding"), m.Content.Body)
	if err != nil {
		t.Fatalf("e-mail %q: cannot decode the body: %v", subject, err)
	}
	out.Body = body
	return out
}

// unfold joins the lines of a folded header value.
func unfold(value string) string {
	return strings.NewReplacer("\r\n", "", "\n", "").Replace(value)
}

// decodeBody decodes a body with its Content-Transfer-Encoding
// (quoted-printable, base64 or none) and returns its text. A multipart body
// yields the text of all its parts; HTML entities are unescaped.
func decodeBody(contentType, encoding, body string) (string, error) {
	var raw []byte
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		b, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
		if err != nil {
			return "", fmt.Errorf("quoted-printable: %w", err)
		}
		raw = b
	case "base64":
		b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(body), ""))
		if err != nil {
			return "", fmt.Errorf("base64: %w", err)
		}
		raw = b
	default:
		raw = []byte(body)
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		// No or invalid Content-Type: RFC 2045 defaults to text/plain.
		return string(raw), nil
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(strings.NewReader(string(raw)), params["boundary"])
		var texts []string
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return "", fmt.Errorf("multipart: %w", err)
			}
			data, err := io.ReadAll(part)
			if err != nil {
				return "", fmt.Errorf("multipart: %w", err)
			}
			// NextPart already decoded quoted-printable parts.
			text, err := decodeBody(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), string(data))
			if err != nil {
				return "", err
			}
			texts = append(texts, text)
		}
		return strings.Join(texts, "\n"), nil
	}
	if mediaType == "text/html" {
		return html.UnescapeString(string(raw)), nil
	}
	return string(raw), nil
}

// normalizeSpace collapses every run of white space in s to one space.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// assertFailureMail checks the failure e-mail of the FAILED video v
// (docs/openapi.yaml, "Failure notification (RF5)"): addressed to email, the
// subject contains the video's original name and the body contains its
// error_message. The body is compared with white space normalized, since
// mail bodies are wrapped.
func assertFailureMail(t *testing.T, m sentMail, email string, v video) {
	t.Helper()
	found := false
	for _, addr := range m.To {
		found = found || addr == strings.ToLower(email)
	}
	if !found {
		t.Errorf("failure e-mail To = %v, want the registered address %s", m.To, email)
	}
	if !strings.Contains(m.Subject, v.OriginalName) {
		t.Errorf("failure e-mail subject %q does not contain the original name %q", m.Subject, v.OriginalName)
	}
	if v.ErrorMessage == nil {
		t.Fatalf("video %s has no error_message to look for in the e-mail", v.ID)
	}
	if !strings.Contains(normalizeSpace(m.Body), normalizeSpace(*v.ErrorMessage)) {
		t.Errorf("failure e-mail body does not contain the error_message %q:\n%s", *v.ErrorMessage, m.Body)
	}
}

// subjects returns the subjects of the mails, for failure messages.
func subjects(mails []sentMail) []string {
	s := make([]string, len(mails))
	for i, m := range mails {
		s[i] = m.Subject
	}
	return s
}
