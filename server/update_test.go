package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/yukiteruamano/artex-global/selfupdate"
)

// releaseCache is the layer that protects the GitHub quota: the unauthenticated API allows only 60 requests/hour/IP,
// and the top bar's "new version available" hint checks once on every full page load. Once the cache fails, a user with a few
// open tabs burns through the quota, and a later real update check cannot get through.

func newTestCache(fetch func(context.Context, *http.Client) (*selfupdate.Release, error)) *releaseCache {
	return &releaseCache{fetch: fetch}
}

func TestReleaseCacheServesFromCache(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	for range 5 {
		rel, err := c.get(t.Context(), nil, false)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if rel.TagName != "v0.3.8" {
			t.Fatalf("TagName = %q", rel.TagName)
		}
	}
	if calls != 1 {
		t.Errorf("5 lookups should hit the origin only once, got %d", calls)
	}
}

func TestReleaseCacheForceBypasses(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// Clicking "Check for updates" must return a live result, otherwise a just-published version stays invisible until the cache expires.
	if _, err := c.get(t.Context(), nil, true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("force should bypass the cache, want 2 origin hits, got %d", calls)
	}
}

func TestReleaseCacheExpiresAfterTTL(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// Move the stored time back to just past expiry to simulate the TTL running out.
	c.at = time.Now().Add(-releaseTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("after the TTL expires it should hit the origin again, want 2, got %d", calls)
	}
}

func TestReleaseCacheUsesShorterTTLForErrors(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return nil, errors.New("github unreachable")
	})

	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("want an error")
	}
	// A failed result is also cached briefly, otherwise every page load waits out a timeout while GitHub is unreachable.
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("want an error")
	}
	if calls != 1 {
		t.Errorf("errors should be cached briefly, want 1 origin hit, got %d", calls)
	}

	// But the error TTL must be clearly shorter than the success TTL, so it self-heals quickly once the network recovers.
	if releaseErrTTL >= releaseTTL {
		t.Fatalf("error TTL (%v) must be shorter than success TTL (%v)", releaseErrTTL, releaseTTL)
	}
	c.at = time.Now().Add(-releaseErrTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("want an error")
	}
	if calls != 2 {
		t.Errorf("after the error TTL expires it should retry, want 2, got %d", calls)
	}
}

func TestReleaseCacheDoesNotPoisonOnCallerCancel(t *testing.T) {
	good := &selfupdate.Release{TagName: "v0.3.8"}
	c := newTestCache(func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return good, nil
	})
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}

	// A visitor closing the tab cancels the request. That does not mean GitHub is broken, so "canceled" must never
	// be written to the cache — otherwise every visitor gets a baffling error for the next 30 minutes.
	c.fetch = func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return nil, ctx.Err()
	}
	c.at = time.Now().Add(-releaseTTL - time.Second) // expire the cache so it has to hit the origin

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx, nil, false); err == nil {
		t.Fatal("a caller cancellation should be passed through to the caller")
	}

	// Key invariant: the canceled attempt leaves no trace — the cache holds neither the "canceled" error
	// and still keeps the previous good result.
	if c.err != nil {
		t.Fatalf("the cancellation error must not be written to the cache, got %v", c.err)
	}
	if c.rel == nil || c.rel.TagName != "v0.3.8" {
		t.Fatalf("the cache should keep the previous good result, got %+v", c.rel)
	}

	// The canceled attempt produced no new data, so the next visitor should hit the origin again — and get a normal result,
	// without being dragged down by the previous cancellation.
	c.fetch = func(context.Context, *http.Client) (*selfupdate.Release, error) {
		return good, nil
	}
	rel, err := c.get(t.Context(), nil, false)
	if err != nil {
		t.Fatalf("a normal request after the cancellation should not fail: %v", err)
	}
	if rel == nil || rel.TagName != "v0.3.8" {
		t.Fatalf("want a normal result, got %+v", rel)
	}
}
