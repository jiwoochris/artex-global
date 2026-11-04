package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"unicode"

	"github.com/yukiteruamano/artex/agent"
)

func TestShutdownContextPreservesNamedCause(t *testing.T) {
	signalCtx, signalCancel := context.WithCancel(context.Background())
	ctx, shutdown := shutdownContext(signalCtx)
	defer shutdown(nil)
	signalCancel()
	<-ctx.Done()
	if code, _, _, ok := agent.AbortReason(ctx); !ok || code != "shutdown" {
		t.Fatalf("code=%q ok=%v, want shutdown", code, ok)
	}
}

// TestPrintBannerLocalized pins the startup banner printed to stdout to its exact
// English text. This is the first thing a user sees when running ARTEX, so its
// wording and layout must not drift, and no stale Korean or Chinese translation may
// come back (backlog F11). The "[config] ..." log lines in run() are intentionally
// out of scope — they are logs, which the localization brief ranks lowest (backlog Z2).
func TestPrintBannerLocalized(t *testing.T) {
	out := captureStdout(t, func() { printBanner(":8787") })
	t.Logf("banner sample:\n%s", out)

	want := banner + fmt.Sprintf("  AI Autonomous Penetration Testing System\n  version %s  ·  %s/%s  ·  %s  ·  listening on %s\n\n",
		version, runtime.GOOS, runtime.GOARCH, runtime.Version(), ":8787")
	if out != want {
		t.Errorf("banner text drifted:\nwant %q\n got %q", want, out)
	}

	for _, r := range out {
		if unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Han, r) {
			t.Errorf("banner has leftover %q (a stale translation leaked back): %q", r, out)
		}
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns whatever
// fn wrote. printBanner uses fmt.Print*, which resolves os.Stdout at call time,
// so swapping it here captures the banner.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}
