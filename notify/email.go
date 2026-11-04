package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// emailDialTimeout / emailSessionTimeout bound connection establishment and the whole
// SMTP session respectively. net/smtp has no timeout mechanism of its own; without these
// two the delivery goroutine hangs forever on a stuck peer — and the dispatcher handles
// deliveries serially in a single goroutine, which would halt the entire notification system.
const (
	emailDialTimeout    = 10 * time.Second
	emailSessionTimeout = 45 * time.Second
)

// emailChannel implements SMTP email delivery.
type emailChannel struct{}

func (emailChannel) Kind() string { return KindEmail }

// Email has no platform rate limit, but it should not be used to flood either; a loose default.
func (emailChannel) DefaultRatePerMin() int { return 60 }

// Only the password is masked. The SMTP host, account and recipients are not secrets, and
// masking them would only make editing harder.
func (emailChannel) SecretKeys() []string { return []string{"password"} }

// host/port decide which server the password is handed to; tls decides whether transport is
// encrypted. Changing any of the three requires restating the password — which also forces
// "turn TLS off" to carry explicit credentials instead of being a casual edit.
func (emailChannel) DestinationKeys() []string { return []string{"host", "port", "tls"} }

func (emailChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "host") == "" {
		return errors.New("SMTP server address is missing")
	}
	port := cfgInt(cfg, "port")
	if port <= 0 || port > 65535 {
		return errors.New("SMTP port is invalid (it must be in the 1-65535 range)")
	}
	if cfgString(cfg, "from") == "" {
		return errors.New("Sender address is missing")
	}
	if len(cfgStrings(cfg, "to")) == 0 {
		return errors.New("At least one recipient address is required")
	}
	return nil
}

func (c emailChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	host := cfgString(cfg, "host")
	port := cfgInt(cfg, "port")
	from := cfgString(cfg, "from")
	to := cfgStrings(cfg, "to")
	username := cfgString(cfg, "username")
	password := cfgString(cfg, "password")
	implicitTLS := cfgBool(cfg, "tls")

	msg, err := buildEmailMessage(from, to, m)
	if err != nil {
		return 0, Permanent(err)
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	client, err := emailDial(ctx, addr, host, implicitTLS)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	// STARTTLS: upgrade if the peer supports it. Credentials must never be sent over a
	// plaintext session (see the auth note below).
	if !implicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return 0, fmt.Errorf("STARTTLS failed: %w", err)
			}
		}
	}
	if username != "" {
		if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			// smtp.PlainAuth refuses to send credentials over an unencrypted connection (unless the
			// target is localhost). That is **correct** security behavior and must not be bypassed,
			// but the reason has to be spelled out — otherwise users only see "unencrypted
			// connection" and do not know what to do.
			if strings.Contains(err.Error(), "unencrypted connection") {
				return 0, Permanent(fmt.Errorf("Refused to send credentials: the connection is not encrypted. Enable TLS, use port 465 (implicit TLS), or check \"Use TLS\" (%w)", err))
			}
			return 0, Permanent(fmt.Errorf("SMTP authentication failed: %w", err))
		}
	}
	if err := client.Mail(from); err != nil {
		return 0, smtpStageError(fmt.Sprintf("The sender address %s was rejected", from), err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return 0, smtpStageError(fmt.Sprintf("The recipient address %s was rejected", rcpt), err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return 0, fmt.Errorf("SMTP DATA failed: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return 0, fmt.Errorf("Failed to write the message body: %w", err)
	}
	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("Failed to submit the message: %w", err)
	}
	// A Quit failure does not change the fact that the message was accepted by the server, so
	// its error is ignored.
	_ = client.Quit()
	// Email bodies are not truncated (the whole HTML body is sent); the whole batch counts as delivered.
	return len(m.Items), nil
}

// emailDial establishes the SMTP connection.
//
// implicitTLS=true takes the "TLS on connect" route such as port 465; false connects in
// plaintext on 25/587 and then runs STARTTLS. The two cannot be mixed: sending a plaintext
// greeting to port 465 gets an immediate disconnect.
//
// The session deadline is set **at connect time** rather than afterwards, because net/smtp's
// Client keeps the underlying connection in an unexported field that callers cannot reach;
// once the connection is handed over, only a pre-set deadline can bound it. That also covers
// a blocking handshake.
// Control attaches blockInternalDial, sharing the same guard as the HTTP-based channels.
// Without it SMTP would be the hole in the whole SSRF defense: a host of 169.254.169.254 or
// 127.0.0.1 connects straight out, and when smtp.NewClient's handshake fails it wraps the
// peer's reply line into the error, which last_error then echoes back through the delivery
// history endpoint — a semi-blind read primitive; the timing difference between "connection
// refused" and "timeout" additionally reveals open ports. The dial stage is the final
// enforcement point and also covers DNS rebinding.
func emailDial(ctx context.Context, addr, host string, implicitTLS bool) (*smtp.Client, error) {
	d := &net.Dialer{Timeout: emailDialTimeout, Control: blockInternalDial}
	var conn net.Conn
	var err error
	if implicitTLS {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("Failed to connect to the SMTP server: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(emailSessionTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SMTP handshake failed: %w", err)
	}
	return client, nil
}

// smtpStageError classifies the failure of one stage as retryable or permanent based on the
// SMTP reply code.
//
// Why the distinction matters: SMTP treats 4xx and 5xx completely differently —
//   - 4xx (450 greylisting, 451 local error, 452 insufficient storage) is a **temporary**
//     rejection; the proper response is to retry later, and greylisting in particular hits
//     almost every first delivery.
//   - 5xx (550 user unknown, 553 bad address) is a permanent rejection; retrying is pointless.
//
// Classifying everything as permanent would make a mail server with greylisting push **every**
// notification into failed after the first attempt — exactly the situation automatic retries
// exist for. The code is taken from the first three digits of the error text; when unavailable
// it is treated as retryable (better one attempt too many than declaring a possible transient
// fault dead because it could not be parsed).
func smtpStageError(what string, err error) error {
	code := smtpReplyCode(err.Error())
	if code >= 500 && code < 600 {
		return Permanent(fmt.Errorf("%s: %w", what, err))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// smtpReplyCode extracts the leading three-digit reply code from SMTP error text and returns
// 0 when there is none. net/smtp does not export the code field, so it has to come from the
// text; the format is "450 4.7.1 ...".
func smtpReplyCode(text string) int {
	if len(text) < 3 {
		return 0
	}
	n, err := strconv.Atoi(text[:3])
	if err != nil {
		return 0
	}
	return n
}

// buildEmailMessage assembles a complete RFC 5322 message.
//
// The body is base64 encoded for two reasons: first, SMTP caps a line at 1000 bytes and HTML
// bodies (digest emails especially) easily exceed that; second, base64 never produces a line
// starting with ".", which removes the need for SMTP dot-stuffing.
func buildEmailMessage(from string, to []string, m Message) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	// A non-ASCII subject must be RFC 2047 encoded, otherwise clients render it as mojibake.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", htmlTitle(m)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	// Email has no hard length limit, so the body is not truncated.
	b.WriteString("\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlBody(m, 0)))
	// base64 wraps at 76 characters, per RFC 2045.
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\r\n")
	return b.String(), nil
}
