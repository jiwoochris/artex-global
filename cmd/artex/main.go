// Command artex runs the ScopeWeaver backend: the PostgreSQL graph stores,
// the event-driven exploration engine, and the JSON HTTP API consumed by the
// shadcn/ui frontend.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/Autumn-27/artex/locale"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/selfupdate"
	"github.com/Autumn-27/artex/server"
)

// version is the build version, injected at release time via
// -ldflags "-X main.version=<tag>". Defaults to "dev" for local builds.
var version = "dev"

const banner = `
  ScopeWeaver
`

// printBanner writes the startup banner + version/runtime info to stdout.
func printBanner(addr string) {
	fmt.Print(banner)
	fmt.Println(locale.Text(locale.ServerDefault(), "  Autonomous AI penetration-testing system"))
	fmt.Printf(locale.Text(locale.ServerDefault(), "  Version %s  ·  %s/%s  ·  %s  ·  Listening on %s\n\n"),
		version, runtime.GOOS, runtime.GOARCH, runtime.Version(), addr)
}

// main only maps run's result onto the process exit code. The exit code is part
// of the update protocol — the supervising start script reads it to decide
// whether to relaunch us (see selfupdate.ExitRestart) — so the body has to live
// in a function that can *return* rather than os.Exit past its own defers.
func main() {
	lang, _ := locale.FromEnv(os.Getenv)
	locale.SetServerDefault(lang)
	os.Exit(run())
}

func run() (code int) {
	var (
		addr    = flag.String("addr", "127.0.0.1:8787", "HTTP listen address (default accepts local connections only; use :8787 to serve other hosts)")
		dataDir = flag.String("data", filepath.Join(config.BaseDir(), "data"), "data directory for SQLite stores (default: data/ next to the executable)")
		proxy   = flag.String("proxy", "127.0.0.1:8788", "traffic recording proxy address (empty to disable)")
	)
	flag.Parse()

	// hand the build version to the server package so GET /api/health can report it
	// to the frontend top bar.
	server.BuildVersion = version

	printBanner(*addr)

	// capture backend logs into the in-memory sink (still to stderr) so the /logs
	// page can show a live log stream. Do this first, to catch startup logs too.
	server.StartLogCapture()

	// Self-update bootstrap: swap in a staged binary, or count a post-swap boot
	// attempt and roll back if the new build keeps dying. Must run before we open
	// the stores or bind a port — this may end with "exit and let the start script
	// relaunch me", and there is no point paying for either first.
	action, upState := selfupdate.Bootstrap()
	server.SetBootUpdateState(upState)
	if action == selfupdate.Restart {
		return selfupdate.ExitRestart
	}

	// surface which config file the binary reads (absolute, so `go run`'s relative
	// "config.json" — resolved against the CWD — is unambiguous).
	cfgPath := config.Path()
	if abs, e := filepath.Abs(cfgPath); e == nil {
		cfgPath = abs
	}
	if _, e := os.Stat(cfgPath); e == nil {
		log.Printf(locale.Text(locale.ServerDefault(), "[config] Configuration file: %s"), cfgPath)
	} else {
		log.Printf(locale.Text(locale.ServerDefault(), "[config] Configuration file: %s (not found; using ARTEX_PG_DSN only)"), cfgPath)
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, shutdown := shutdownContext(sigCtx)
	defer shutdown(agent.AbortShutdown)

	mgr, err := server.NewManager(*dataDir, *proxy)
	if err != nil {
		log.Printf("open stores: %v", err)
		return 1
	}
	defer func() {
		if err := mgr.Close(); err != nil {
			log.Printf("close stores: %v", err)
			code = 1
		}
	}()

	// Surviving this long means a freshly swapped-in build actually works, so drop
	// the upgrade marker and stop counting attempts. Until it fires, every boot
	// increments the count and a build that keeps dying gets rolled back.
	settle := time.AfterFunc(selfupdate.SettleDelay, selfupdate.Settle)
	defer settle.Stop()

	skillDir := config.SkillDir()
	if abs, err := filepath.Abs(skillDir); err == nil {
		skillDir = abs
	}
	log.Printf(locale.Text(locale.ServerDefault(), "[config] Skill directory: %s"), skillDir)
	srv := server.New(ctx, mgr, skillDir, *dataDir, config.BaseDir())
	srv.WarnIfExposed(*addr)
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	defer func() {
		shutdown(agent.AbortShutdown)
		code = shutdownServer(code, httpSrv, srv.DrainChats)
	}()
	served := make(chan error, 1)
	go func() {
		log.Printf("ScopeWeaver %s backend listening on %s (data=%s, workers=%d)", version, *addr, *dataDir, mgr.Workers())
		served <- httpSrv.ListenAndServe()
	}()

	// A signal stops normally (exit 0); a staged update or rollback requests
	// relaunch (exit 75). A serve failure exits nonzero after the same cleanup.
	select {
	case <-ctx.Done():
	case <-server.RestartRequested():
		code = selfupdate.ExitRestart
		shutdown(agent.AbortShutdown)
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("serve: %v", err)
			code = 1
		}
	}

	return code
}

func shutdownServer(code int, httpSrv *http.Server, drain func(context.Context) error) int {
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown: %v", err)
		_ = httpSrv.Close()
		code = 1
	}
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer drainCancel()
	if err := drain(drainCtx); err != nil {
		log.Printf("drain background chats: %v", err)
		code = 1
	}
	return code
}

// shutdownContext deliberately does not derive from signalCtx. If it did, the
// parent's plain context.Canceled could win the race before AbortShutdown was
// attached to the child, losing the diagnostic cause in every running Agent.
func shutdownContext(signalCtx context.Context) (context.Context, context.CancelCauseFunc) {
	ctx, shutdown := context.WithCancelCause(context.Background())
	go func() {
		select {
		case <-signalCtx.Done():
			shutdown(agent.AbortShutdown)
		case <-ctx.Done():
		}
	}()
	return ctx, shutdown
}
