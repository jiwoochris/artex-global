package notify

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// This file covers two related hardening measures:
//   1) a delivery address must not use the server as a pivot into the internal network / cloud metadata (SSRF)
//   2) the error message from address validation must not bring out the credential inside the address
//
// About the test environment: many cases in this package use fake httptest receivers on 127.0.0.1, which the guard blocks by default,
// so TestMain turns on AllowLocalTargetsEnv globally, while every SSRF case below clears it explicitly
// to assert the **default deny** behaviour.

func TestMain(m *testing.M) {
	// Let normal cases reach the local fake receiver; the SSRF cases clear it themselves for the moment.
	_ = os.Setenv(AllowLocalTargetsEnv, "1")
	os.Exit(m.Run())
}

// TestDialGuardRejectsLoopbackByDefault is the core assertion of SSRF protection:
// under the default config, delivery to a loopback address must be refused by the **connection layer**.
func TestDialGuardRejectsLoopbackByDefault(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "") // turn off the escape hatch = default behaviour
	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("delivery to a loopback address must not be allowed by default")
	}
	if hit {
		t.Fatal("the request already reached the local service -- the guard did not take effect")
	}
	// The error message must tell the user how to opt out (a local SMTP relay is a legitimate config).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("the refusal message should explain how to opt out explicitly: %v", err)
	}
}

// TestDialGuardAllowsLoopbackWhenOptedIn is the reverse case: once opted in explicitly it must work,
// otherwise legitimate deployments like a local postfix / intranet relay get wiped out indiscriminately.
func TestDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("delivery should be allowed once explicitly enabled: %v", err)
	}
}

func TestIsBlockedDialIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"169.254.169.254", // cloud metadata endpoint — the main reason this function exists
		"169.254.1.1", "fe80::1",
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1",
		"::ffff:127.0.0.1", // IPv4-mapped form must be unmapped before the check, otherwise it is a bypass
		"",
	}
	for _, s := range blocked {
		if !isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s should be rejected", s)
		}
	}
	// RFC1918 private ranges are **deliberately allowed**: a self-hosted Mattermost / SMTP relay on the intranet is a common legitimate use.
	// This assertion pins that trade-off down — if someone casually adds a private-range check later, this fails,
	// forcing a conscious decision (instead of silently breaking a batch of deployments).
	allowed := []string{"10.0.0.5", "172.16.3.4", "192.168.1.10", "8.8.8.8", "2606:4700::1111"}
	for _, s := range allowed {
		if isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed (private networks are a common legitimate delivery target)", s)
		}
	}
}

// TestValidateHTTPURLRejectsLiteralPrivateTargets covers the config-time pre-check:
// a literal IP should be rejected when saved, not when the first delivery fails.
func TestValidateHTTPURLRejectsLiteralPrivateTargets(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "")
	for _, raw := range []string{
		"http://127.0.0.1:8080/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:8080/hook",
	} {
		if err := validateHTTPURL(raw); err == nil {
			t.Errorf("%s should be rejected at config time", raw)
		}
	}
	// Public and private addresses pass as usual (private ranges are left to the dial stage, which does not block them).
	for _, raw := range []string{"https://oapi.dingtalk.com/robot/send", "http://10.0.0.9/hook"} {
		if err := validateHTTPURL(raw); err != nil {
			t.Errorf("%s should pass validation: %v", raw, err)
		}
	}
}

// TestValidateHTTPURLErrorNeverLeaksCredentials is the branch the audit pointed out I missed last round.
//
// When url.Parse **fails** it returns *url.Error, whose Error() contains the full raw address. Last round I only
// redacted the errors returned by http.Client.Do and missed this one; the "permanent failure path" cases added back then
// (file://, gopher://, ftp://) actually all parse successfully in url.Parse and take the scheme branch,
// so an all-green run proves nothing about this path — a false guarantee.
func TestValidateHTTPURLErrorNeverLeaksCredentials(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/%zz?access_token=" + leakProbeToken,         // invalid percent escape
		"https://a.example.com:port/x?access_token=" + leakProbeToken, // port is not a number
		"http://[::1?access_token=" + leakProbeToken,                  // unbalanced bracket
	}
	for _, raw := range cases {
		// First confirm this input **really** makes url.Parse fail. Without this step the case could
		// silently take a different branch (that is exactly how last round's false guarantee happened).
		if _, err := url.Parse(raw); err == nil {
			t.Errorf("%q should fail to parse, otherwise this case does not cover the target branch", raw)
			continue
		}
		err := validateHTTPURL(raw)
		if err == nil {
			t.Errorf("%q should fail validation", raw)
			continue
		}
		assertNoSecret(t, err.Error(), leakProbeToken)
	}
	// Confirm the channel-layer wrapping does not carry the address out either.
	t.Setenv(AllowLocalTargetsEnv, "")
	err := (dingTalkChannel{}).Validate(map[string]any{"webhook": cases[0]})
	if err == nil {
		t.Fatal("an invalid address should fail validation")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestEmailDialGuardRejectsLoopbackByDefault covers the dial guard of the SMTP channel.
//
// The email channel used to dial with a bare net.Dialer, the only hole in the whole SSRF protection: setting host to
// 169.254.169.254 or 127.0.0.1 connected straight through, and when the smtp.NewClient handshake failed it wrapped
// the line returned by the peer into the error, which last_error echoed through the delivery history API — exactly the
// semi-blind read primitive the other channels had already closed; the timing difference between "connection refused" and "timeout" could also probe ports.
//
// This package's TestMain turns AllowLocalTargetsEnv on globally (many cases use a fake receiver on
// 127.0.0.1), so this case must clear it itself — otherwise it passes whether the guard exists or not,
// which is exactly why no test ever caught the hole.
func TestEmailDialGuardRejectsLoopbackByDefault(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "") // turn off the escape hatch = default behaviour
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Fatal("mail must not be delivered to a loopback address by default")
	}
	// The connection must never be established: the guard blocks it in the Control hook, so EHLO never goes out.
	if f.sawCommand("EHLO") || f.sawCommand("HELO") {
		t.Fatal("an SMTP session was established — the guard had no effect")
	}
	// The error message must tell the user how to opt in (a local postfix relay is a legitimate configuration).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("the rejection message should explain how to opt in explicitly: %v", err)
	}
}

// TestEmailDialGuardAllowsLoopbackWhenOptedIn is the paired reverse case: once explicitly enabled
// delivery must work normally. A self-hosted intranet SMTP / local relay is a very common deployment, so the guard must not block indiscriminately.
func TestEmailDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("local SMTP delivery should work once explicitly enabled: %v", err)
	}
	if !f.sawCommand("EHLO") {
		t.Fatal("no EHLO seen — the session was not really established")
	}
}
