package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
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

// TestPrintBannerIsEnglish verifies that the startup banner printed to stdout is
// English, like the console logs that follow it.
func TestPrintBannerIsEnglish(t *testing.T) {
	out := captureStdout(t, func() { printBanner(":8787") })
	for _, r := range out {
		if r > 127 {
			t.Fatalf("banner must be ASCII English: %q", out)
		}
	}
	for _, want := range []string{"autonomous penetration testing system", "version", "listening on", ":8787"} {
		if !strings.Contains(out, want) {
			t.Errorf("banner is missing %q: %q", want, out)
		}
	}
}

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
