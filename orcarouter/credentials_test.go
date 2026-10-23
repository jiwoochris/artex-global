package orcarouter

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeConnector struct {
	result ExchangeResult
	err    error
}

func (f fakeConnector) Connect(context.Context) (ExchangeResult, error) { return f.result, f.err }

// TestBothAdaptersYieldTheSameCredentialShape proves the provider and model
// discovery can consume either source identically.
func TestBothAdaptersYieldTheSameCredentialShape(t *testing.T) {
	api, err := APIKeyAdapter{Key: "  sk-orca-test-manual  "}.Acquire(context.Background())
	if err != nil {
		t.Fatalf("api key adapter: %v", err)
	}
	var persisted string
	pkce, err := PKCEAdapter{
		Connector: fakeConnector{result: ExchangeResult{Key: "sk-orca-test-oauth", Scope: "api"}},
		Persist:   func(_ context.Context, key string) error { persisted = key; return nil },
	}.Acquire(context.Background())
	if err != nil {
		t.Fatalf("pkce adapter: %v", err)
	}
	if api.Key != "sk-orca-test-manual" || pkce.Key != "sk-orca-test-oauth" {
		t.Fatalf("unexpected keys: %q %q", api.Key, pkce.Key)
	}
	if api.Source == pkce.Source {
		t.Fatal("sources must be distinguishable for UI/audit")
	}
	if persisted != pkce.Key {
		t.Fatal("PKCE key was not persisted before returning")
	}
	// Downstream code only sees Key: the same helper must accept both.
	if got := credentialAPIKey(api); got != api.Key {
		t.Fatalf("downstream api key = %q", got)
	}
	if got := credentialAPIKey(pkce); got != pkce.Key {
		t.Fatalf("downstream api key = %q", got)
	}
}

func credentialAPIKey(c Credential) string { return c.Key }

func TestAPIKeyAdapterRejectsEmptyAndWhitespace(t *testing.T) {
	if _, err := (APIKeyAdapter{Key: "   "}).Acquire(context.Background()); err == nil {
		t.Fatal("empty key accepted")
	}
	if _, err := (APIKeyAdapter{Key: "sk-orca-a b"}).Acquire(context.Background()); err == nil {
		t.Fatal("key with whitespace accepted")
	}
}

func TestPKCEAdapterPersistFailureSurfaces(t *testing.T) {
	_, err := PKCEAdapter{
		Connector: fakeConnector{result: ExchangeResult{Key: "sk-orca-test-oauth", Scope: "api"}},
		Persist:   func(context.Context, string) error { return errors.New("store offline") },
	}.Acquire(context.Background())
	if err == nil || !strings.Contains(err.Error(), "storing") {
		t.Fatalf("err = %v", err)
	}
}

func TestPKCEAdapterPropagatesDenial(t *testing.T) {
	_, err := PKCEAdapter{Connector: fakeConnector{err: ErrDenied}}.Acquire(context.Background())
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v", err)
	}
}

func TestRegistryGenerationSafety(t *testing.T) {
	var reg Registry
	gen1 := reg.Set(Credential{Key: "sk-orca-test-one", Source: SourceAPIKey})
	if _, err := reg.Current(); err != nil {
		t.Fatalf("current: %v", err)
	}
	if !reg.MarkUnauthorized(gen1) {
		t.Fatal("current generation should be markable")
	}
	if _, err := reg.Current(); !errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("current err = %v, want ErrNeedsReauth", err)
	}
	// A stale 401 from an old generation must never poison the new credential.
	gen2 := reg.Set(Credential{Key: "sk-orca-test-two", Source: SourcePKCE})
	if reg.MarkUnauthorized(gen1) {
		t.Fatal("stale generation must not mark the new credential")
	}
	if reg.NeedsReauth() {
		t.Fatal("reconnect must clear needsReauth")
	}
	cur, err := reg.Current()
	if err != nil || cur.Key != "sk-orca-test-two" || cur.Generation != gen2 {
		t.Fatalf("current = %+v err=%v", cur, err)
	}
}

func TestClassifyStatusOnlyRevokesOn401(t *testing.T) {
	if !errors.Is(ClassifyStatus(401), ErrNeedsReauth) {
		t.Fatal("401 must be terminal reauth")
	}
	if errors.Is(ClassifyStatus(403), ErrNeedsReauth) {
		t.Fatal("403 must not revoke the credential")
	}
	if !errors.Is(ClassifyStatus(429), ErrRateLimited) {
		t.Fatal("429 must be transient")
	}
}

func TestMaskKey(t *testing.T) {
	got := MaskKey("sk-orca-abcdefghijklmnop")
	if strings.Contains(got, "abcdefghij") {
		t.Fatalf("mask leaked body: %q", got)
	}
	if !strings.HasSuffix(got, "mnop") {
		t.Fatalf("mask should keep last 4: %q", got)
	}
	if MaskKey("short") != "••••" {
		t.Fatal("short keys must be fully masked")
	}
}

func TestResolveEndpointsPrecedence(t *testing.T) {
	t.Setenv("ORCA_BASE_URL", "https://self.example")
	t.Setenv("ORCA_AUTH_BASE_URL", "")
	t.Setenv("ORCA_API_BASE_URL", "")
	eps, err := ResolveEndpoints()
	if err != nil {
		t.Fatal(err)
	}
	if eps.AuthBase != "https://self.example" || eps.APIBase != "https://self.example/v1" {
		t.Fatalf("shared fallback = %+v", eps)
	}

	t.Setenv("ORCA_AUTH_BASE_URL", "https://auth.example")
	t.Setenv("ORCA_API_BASE_URL", "https://infer.example/v1")
	eps, err = ResolveEndpoints()
	if err != nil {
		t.Fatal(err)
	}
	if eps.AuthBase != "https://auth.example" || eps.APIBase != "https://infer.example/v1" {
		t.Fatalf("explicit overrides = %+v", eps)
	}

	t.Setenv("ORCA_BASE_URL", "http://remote.example")
	t.Setenv("ORCA_AUTH_BASE_URL", "")
	t.Setenv("ORCA_API_BASE_URL", "")
	if _, err := ResolveEndpoints(); err == nil {
		t.Fatal("http remote origin must be rejected")
	}
}
