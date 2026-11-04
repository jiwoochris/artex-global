# Security review — artex-global

> **Status (2026-11).** The container findings in this review (H-2, H-3, M-7) were remediated by
> consolidating the separate "hardened" stack into the single `docker-compose.yml`: the unprivileged
> user, `cap_drop: ALL` + `NET_RAW`, `no-new-privileges`, private database network, capped
> processes/logs/tmpfs and loopback-only ports are now the default, the Node build no longer runs a
> remote installer, and base images are digest-pinned. `Dockerfile.hardened`,
> `docker-compose.hardened.yml` and `docs/docker-hardened.md` no longer exist, so the `file:line`
> references to them below describe the pre-consolidation tree and are kept as the audit record.
> `deploy/posture_test.go` now enforces the posture in CI.


**Branch:** `chore/i18n-en-es-hardening` (HEAD `fdfb7a1`)
**Date:** 2026-11-03
**Type:** static/manual review + automated scanners. No repository file other than this report was modified;
no code, build or test command was executed.

---

## 1. Scope & method

### 1.1 What was reviewed

| Area | Files / packages |
|---|---|
| Auth / session | `server/auth.go`, `server/server.go` (`Handler`, `requireAuth`, `cors`) |
| Path handling / archives | `server/skill_zip.go`, `server/task_archive_package.go`, `server/chatupload.go`, `server/workspace.go`, `server/server_mgmt.go` (skill FS), `db/config.go` |
| Command / tool execution | `agent/tools.go`, `agent/toolcatalog.go`, `agent/{worker,planner,mainagent,chat,goals,provider}.go`, `server/platform_tools.go`, `server/customtool.go`, `guard/guard.go`, `intercept/` |
| SSRF / outbound | `notify/http.go`, `notify/ssrf_test.go`, `selfupdate/{github,stage,bootstrap,selfupdate}.go`, `mcphttp/client.go`, `traffic/traffic.go` (go-mitmproxy) |
| Secrets & logging | `server/logsink.go`, `notify/mask.go`, `evidence/store.go`, `agent/capture.go`, `llmrec/capture.go` |
| SQL | `db/**` (schema + all query builders) |
| Web | `web/src/proxy.ts`, `web/next.config.mjs`, `web/src/lib/{auth,api,cookie.client}.ts` |
| Container / CI | `Dockerfile`, `Dockerfile.hardened`, `docker-compose.yml`, `docker-compose.hardened.yml`, `.github/workflows/*.yml` |

### 1.2 Tools and versions

| Tool | Version | Output |
|---|---|---|
| `golang.org/x/vuln/cmd/govulncheck@latest` | latest (fetched by `go run`), Go toolchain `go1.27.3 linux/amd64` | `/tmp/pi/sec/govulncheck.txt`, `/tmp/pi/sec/govulncheck-verbose.txt` |
| `github.com/securego/gosec/v2/cmd/gosec@latest` | `dev` | `/tmp/pi/sec/gosec.json` (raw), `/tmp/pi/sec/gosec.log` |
| `pnpm audit --json` | pnpm 12.16.1, 850 total deps | `/tmp/pi/sec/pnpm-audit.json` |

> Note on `gosec` exit status: gosec exits 1 when it reports issues; the JSON report was still produced
> in full (178 files / 66 094 lines scanned, 254 findings, `nosec: 0`).

### 1.3 Method

1. Ran the three scanners above (read-only; all outputs under `/tmp/pi/sec/`).
2. Read every file in the scope list end-to-end, following a model-supplied or HTTP-supplied value from
   its entry point (route / tool call / file) to the syscall or network call that consumes it.
3. For agent-shell semantics the dependency source was read directly in the module cache
   (`github.com/Autumn-27/norma@v0.4.4/tool/bash.go`, `permission/permission.go`, `harness/tools.go`),
   because the tool *handlers* live there, not in this repository.
4. Every finding below cites a `file:line` that was read, and quotes the code that creates the issue.
   Sanner hits that could not be reproduced as a real weakness are listed in §5, not in the findings table.

---

## 2. Findings

Severity counts: **Critical 0 · High 4 · Medium 9 · Low 5**.

| ID | Severity | Area | Location | Impact | Fix |
|---|---|---|---|---|---|
| H-1 | High | Tool execution | `agent/worker.go:446`, `guard/guard.go:4-7`, norma `tool/bash.go:160` | Model output reaches `sh -c` with no sandbox and no authorization-scope check; prompt-injected target content → code execution as the server user | Add an out-of-band scope/egress enforcement point; run the agent as a different UID from the data owner |
| H-2 | High | Container posture | `Dockerfile` (no `USER`), `docker-compose.yml:42-54` | Default image runs as **root** with `./data` + `./skills` bind-mounted from the host, writable rootfs, no capability drop | Ship the hardened image as default: `USER`, `read_only`, `cap_drop: [ALL]`, named volumes |
| H-3 | High | Network exposure | `Dockerfile:64` (`-proxy :8788`), `traffic/traffic.go:204` | Unauthenticated TLS-intercepting open proxy bound to every container interface | Bind `127.0.0.1:8788` in the default CMD, as `Dockerfile.hardened:110` already does |
| H-4 | High | Path handling | `server/workspace.go:44-59`, `server/server_mgmt.go:1666-1795` | Containment is lexical only; a symlink inside an agent-writable root escapes it → arbitrary file read/write via the authenticated API (incl. `jwt.key`, which is deliberately kept outside the work dir) | Resolve symlinks (`filepath.EvalSymlinks`) and re-check containment; or `os.Root` (Go 1.24+) |
| M-1 | Medium | Web / session | `web/src/lib/auth.ts:21-23`, `web/src/lib/api.ts:121-123` | JWT lives in `localStorage` **and** in a JS-readable, non-`Secure` cookie; any XSS = permanent admin takeover; token travels in clear text (server is HTTP-only) | `HttpOnly; Secure; SameSite=Strict` cookie set by the Go server; stop putting the JWT in `localStorage` |
| M-2 | Medium | Web / headers | `server/server.go:1066-1069`, `web/next.config.mjs` | No CSP, `X-Frame-Options`, `nosniff`, `HSTS` or `Referrer-Policy` anywhere; clickjacking and sniffing-XSS are unmitigated and M-1's blast radius is maximal | One header middleware on the API/UI handler |
| M-3 | Medium | Guard bypass | `guard/guard.go:5-7`, `db/db.go:540-545`, `intercept/intercept.go:159-163` | The safety floor is 9 user-deletable regexes over tool-input JSON, trivially evadable; MCP tools and custom tools are not in the enabled-tool set at all | Move destructive/exfil gating out of regex into a real policy layer; include MCP/custom tools in the intercept set |
| M-4 | Medium | Agent persistence | `server/platform_tools.go:294` (`create_mcp`), `server/mcpdiscover.go:43-47`, `server/platform_tools.go:123` | A prompt-injected agent can persist an arbitrary stdio command (`npx …`), env vars and custom Python that execute at every start-up | Require explicit user approval for `create_mcp` / `create_custom_tool` / `update_skill_file` (they are `Allowed()` today) |
| M-5 | Medium | Secrets at rest / egress | `traffic/traffic.go:60-68`, `traffic/traffic.go:653-668`, `traffic/traffic.go:950-975` | Captured third-party credentials (`Authorization`, `Cookie`) are written unencrypted to `data/traffic/index.db` **and** returned to the model, i.e. sent to a third-party LLM provider | Redact configurable header names at capture time; document the data-egress fact; encrypt the traffic store |
| M-6 | Medium | Logging | `server/auth.go:293`, `server/logsink.go:56-90` | The first-run setup token is printed into the ring buffer, persisted to `server_logs` and served by `/api/logs` | Log it to stderr only, never through the sink |
| M-7 | Medium | Supply chain (build) | `Dockerfile:24`, `Dockerfile.hardened:68` | `curl -fsSL https://deb.nodesource.com/setup_22.x \| bash -` runs an unpinned remote script as root at build time; base images are tag-pinned, not digest-pinned | Pin base images by digest; install Node from a pinned distro/signed repo |
| M-8 | Medium | CI | `.github/workflows/release.yml:6-7`, all workflows | `release.yml` grants `contents: write` to *every* job including the `pnpm install` frontend job; all actions are pinned to floating major tags | Per-job `permissions:`; pin actions by commit SHA |
| M-9 | Medium | TLS | `mcphttp/client.go:99-107`, `mcphttp/client.go:129-131` | `insecure: true` disables certificate verification on MCP connections that carry `Authorization` / API-key headers | Reject `insecure` when credential headers are configured; or require an explicit fingerprint |
| L-1 | Low | Auth surface | `server/auth.go:445-453` | The JWT is accepted via `?token=` on **every** endpoint, not just SSE → tokens leak into Referer, proxy logs and browser history | Only accept `?token=` on the two SSE routes |
| L-2 | Low | Web dev config | `web/next.config.mjs:19`, `server/server.go:4086` | `allowedDevOrigins: ["*.*.*.*"]` and a default CORS allowlist of `localhost:5173` widen dev-mode exposure | Scope both to `127.0.0.1`/the dev host |
| L-3 | Low | Update integrity | `selfupdate/stage.go:134-155`, `selfupdate/github.go:23-31` | `SHA256SUMS` is fetched from the same unsigned channel as the artifact — a compromised release serves a matching sum | Sign `SHA256SUMS` (minisign/Sigstore) and verify the signature |
| L-4 | Low | HTTP server | `cmd/artex/main.go:117-121` | Only `ReadHeaderTimeout` is set; no `ReadTimeout`/`IdleTimeout`, no request-body cap on most JSON handlers (`server/server_mgmt.go:150`) | Set `ReadTimeout`/`IdleTimeout`; wrap the default JSON `decode` in `MaxBytesReader` |
| L-5 | Low | File modes | `server/workspace.go:174`, `server/chatupload.go:98`, `traffic/traffic.go:552` | Work dir, uploads and traffic evidence are created `0755`/`0644`, so they are world-readable on a shared host | `0750`/`0600` (gosec G301/G306) |

---

## 3. Finding detail (ordered by severity)

### H-1 — The agent's shell has no sandbox and no authorization scope

**Verify the control first, because the marketing intent is different.** `guard/guard.go` states its own
limits in the package comment:

```go
// guard/guard.go:1-7
// Package guard implements the safety boundary layer (docs §11): an audit log,
// user-configured intercept-rule evaluation, and Observer/G5 failure attribution.
// Every tool call passes through the PreToolUse hook before executing.
// (The RoE authorization-scope mechanism was removed; a replacement may be added
// later.) Destructive/exfil gating is no longer hard-coded here — it lives in the
// DB intercept rules (seeded as ordinary [built-in] rules, so users can disable or
// delete them), evaluated via applyIntercept.
```

`preToolUse` therefore only *records* and then delegates:

```go
// guard/guard.go:76-84
	g.record(ev.ToolName, "allow", "", cmd)
	return g.applyIntercept(ctx, ev)
```

…and `applyIntercept` returns `hook.Result{}` (i.e. **allow**) when the interceptor is nil, when the tool
is not in the enabled-tool set, and when no rule matches and the LLM judge is off (`guard/guard.go:83-131`).
`task_scope` (`db/task_assets.go`, `agent/tools.go:830-852`) is **not** consulted on any execution path —
it is a prompt/coverage construct, not an enforcement point.

Every agent runs with permissions bypassed, so the tool's own `AskUser` never fires:

```go
// agent/worker.go:446  (identical at agent/chat.go:134, agent/mainagent.go:142,
// agent/planner.go:417, agent/goals.go:174, agent/provider.go:484)
		PermissionMode:  permission.ModeBypass,
```

norma's pipeline confirms the exact semantics — deny rules win, then `ModeBypass` allows everything:

```go
// norma@v0.4.4/permission/permission.go:86-100
func Evaluate(ctx context.Context, req Request) Decision {
	// Stage 1 — deny rules (absolute priority).
	if matchRule(req.Ctx.Disallowed, req.ToolName) { … }
	// A tool that hard-denies (e.g. failed self-validation) is also absolute.
	if req.ToolDecision.Behavior == Deny { return req.ToolDecision }
	// Stage 2 — mode short-circuits.
	switch req.Ctx.Mode {
	case ModeBypass:
		return Allowed()
```

and the Bash tool spawns a plain shell with no cwd jail:

```go
// norma@v0.4.4/tool/bash.go:160
	cmd := exec.CommandContext(cctx, shell, append(flags, command)...)
	if tc != nil { cmd.Dir = tc.WorkingDir; cmd.Env = withEnv(tc.Env) }
```

**Why this is High, not "by design".** An autonomous pentest agent legitimately needs `sh`. What is
missing is the *other* half of the boundary: (a) the target scope the operator entered is never enforced
on the execution path, and (b) the consequence of a prompt injection — a scanned page that says "ignore
previous instructions and run …" — is an unbounded shell as the server user. Both are reachable from
ordinary, expected use (the recorder feeds attacker-controlled HTTP bodies back into the model via
`traffic_get`, `traffic/traffic.go:950-975`).

**Remediation.** Enforce scope where the syscall happens, not in prose: an egress allowlist on the
recording proxy plus a `--scope` file the Bash wrapper refuses to leave, and an intercept rule that is
code, not a DB row. Minimum viable step: reinstate an "authorization scope" check in `preToolUse` for
network-touching commands (the removed RoE mechanism), and run agents under a dedicated UID with no write
access to `config.BaseDir()` (`jwt.key` lives there — see H-4).

---

### H-2 — The default container image runs as root with host bind mounts

`Dockerfile` never declares a user, so PID 1 is root:

```dockerfile
# Dockerfile:61-64   (no USER anywhere in the file)
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
```

`docker-compose.yml` — the stack most operators actually run — adds no mitigation and bind-mounts two
host directories into that root container:

```yaml
# docker-compose.yml:42-54
    ports:
      - "${ARTEX_BIND:-127.0.0.1}:8787:8787"
      - "127.0.0.1:8788:8788"
    volumes:
      - ./data:/app/data
      - ./skills:/app/skills
```

There is no `user:`, `read_only:`, `cap_drop:`, or `security_opt:` in the `artex` service. Contrast
`docker-compose.hardened.yml:57-148`, which does all of it correctly (`user: "10001:10001"` at line 59,
`read_only: true` at 66, `cap_drop: [ALL]` at 104, `security_opt: [no-new-privileges:true]`, named volumes) — so the
project already contains the right design, it is simply not the default.

**Impact.** H-1 + H-2 compose: a model-driven `rm -rf` or a `chown`/`chmod`, or any code execution in the
agent, runs as root and can destroy anything under the host's `./data` and `./skills`, and any
root-writable file inside the container.

**Remediation.** Make `Dockerfile.hardened` + `docker-compose.hardened.yml` the documented default, or
copy its three security directives into `Dockerfile`/`docker-compose.yml`. `start.sh` should re-exec the
binary as `nobody` if it detects `id -u == 0`.

---

### H-3 — Unauthenticated TLS-intercepting proxy on `0.0.0.0:8788` in the shipped image

```dockerfile
# Dockerfile:64
CMD ["-addr", ":8787", "-proxy", ":8788"]
```

The binary default is loopback (`cmd/artex/main.go:57`: `"127.0.0.1:8788"`), and the hardened image keeps
it (`Dockerfile.hardened:110`: `"-proxy", "127.0.0.1:8788"` with an explicit comment: *"That proxy has no
authentication, so it is pinned to container loopback … nothing else on the Docker network can use it as
an open proxy"*). The published default image overrides exactly that protection.

There is no authenticator in the proxy construction — every request is accepted and MITM'd:

```go
// traffic/traffic.go:204-209
	p, err := mproxy.NewProxy(&mproxy.Options{
		Addr:        addr,
		SslInsecure: true,
		CaRootPath:  filepath.Join(dir, "_ca"),
	})
```

**Impact.** Anyone who can reach port 8788 of the container (any peer on the Docker network, any host
process when the port is published, or anyone on the LAN if `-addr` is widened) gets a fully permissive
forward proxy that also terminates TLS to arbitrary targets. Combined with the MITM CA in
`data/traffic/_ca`, it is a ready-made credential-interception point.

**Remediation.** Change `Dockerfile:64` to `-proxy 127.0.0.1:8788`. If an external proxy is genuinely
wanted, add SOCKS/HTTP basic auth to `mproxy.Options` and require it in `traffic.Open`.

---

### H-4 — Workspace / skill containment is lexical; symlinks escape it

```go
// server/workspace.go:44-59
func (s *Server) wsResolve(rel string) (string, bool) {
	base := filepath.Clean(s.m.dir)
	rel = strings.TrimPrefix(strings.TrimSpace(rel), "/")
	clean := filepath.Clean("/" + rel) // e.g. "/a/../../etc" → "/etc" (still rooted at "/")
	abs := filepath.Clean(filepath.Join(base, clean))
	if abs != base && !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		return "", false
	}
	return abs, true
}
```

`filepath.Clean` is pure string manipulation: it neither calls `Lstat` nor `EvalSymlinks`. Every consumer
then follows links — `os.Stat` (`workspace.go:126`), `os.ReadFile` (`workspace.go:181`),
`os.WriteFile` (`workspace.go:216`), `http.ServeFile` (`workspace.go:262`). So for a path
`data/escape` that is a symlink to `/`:

* `GET /api/workspace/read?path=escape/etc/passwd` passes the check (`abs = <data>/escape/etc/passwd`
  has the required prefix) and returns `/etc/passwd`.
* `POST /api/workspace/write` with the same shape **writes** outside the work dir.

The same pattern exists in the skill browser (`server/server_mgmt.go:1666-1795`), where `skillRelPath`
(`server/server_mgmt.go:1582-1614`) validates characters and `..` but never the resolved target.

This defeats a protection the code explicitly claims. `server/auth.go:73-76` documents that the signing
key was moved **out of** the browsable workspace precisely so it cannot be listed or downloaded:

```go
// server/auth.go:73-76 (abridged)
// loadOrCreateJWTKey reads the 32-byte signing key from keyDir/jwt.key. keyDir is
// the project base dir (next to the executable), NOT the browsable workspace root
// (dataDir) — the signing key must never be listable/downloadable via the file
// manager. …
```

A one-line `ln -s .. <data>/up` (which the agent can trivially create through Bash, H-1) turns that into
`GET /api/workspace/download?path=up/jwt.key`.

**Impact.** Arbitrary file read/write as the server user through an authenticated API — and root, in the
default image (H-2). The "confined to the work dir" property claimed at `server/workspace.go:16-19` does
not hold.

**Remediation.** After the lexical check, resolve and re-check:

```go
real, err := filepath.EvalSymlinks(abs)          // for reads
real, err  = filepath.EvalSymlinks(filepath.Dir(abs)) // for writes (target may not exist)
if err != nil || (real != base && !strings.HasPrefix(real, base+string(os.PathSeparator))) { … }
```

or move the whole file manager onto `os.Root` / `os.OpenRoot` (Go 1.24+), which refuses to leave the root
by construction. Also reject any path whose `Lstat` mode has `os.ModeSymlink` set.

---

### M-1 — Session token in `localStorage` and in a non-`HttpOnly`, non-`Secure` cookie

```ts
// web/src/lib/auth.ts:20-24
  setToken(token: string): void {
    localStorage.setItem(TOKEN_KEY, token);
    // Write the cookie synchronously so Next.js middleware can read it server-side
    document.cookie = `${TOKEN_KEY}=${encodeURIComponent(token)}; path=/; max-age=${COOKIE_MAX_AGE}; SameSite=Lax`;
  },
```

```ts
// web/src/lib/api.ts:121-124
function getToken(): string | null {
  if (typeof window === "undefined") return null;
  return localStorage.getItem("artex_token");
}
```

and the API accepts that token from the cookie as well, server-side:

```go
// server/auth.go:444-453
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") { … }
	if c, err := r.Cookie("artex_token"); err == nil && c.Value != "" { return c.Value }
	return r.URL.Query().Get("token")
}
```

Facts, precisely stated:

* `HttpOnly` is impossible here — the cookie is written by JavaScript. Any script on the origin reads it.
* `Secure` is absent, and the server never serves TLS (`cmd/artex/main.go:117-125` is `ListenAndServe`
  only), so the token is sent in clear text on the default `http://127.0.0.1:8787` and on any plain-HTTP
  reverse proxy.
* `SameSite=Lax` is present and is the one cookie attribute that is correct.
* The JWT is valid for 7 days (`jwtTTL`, `server/auth.go:27`) and the only revocation is a password change
  (`signingKey` binds the HMAC key to the bcrypt hash — a genuinely good design, see §5).

**How weak, exactly.** I searched for a remote-controlled XSS sink and did **not** find one: `react-markdown`
is used without `rehype-raw`, the two `dangerouslySetInnerHTML` sites (`web/src/components/ui/chart.tsx:95`,
`web/src/scripts/theme-boot.tsx:112`) are build-time/admin-controlled, and the report exports are served
with `Content-Disposition: attachment`. So this is the *blast-radius* control, not an independently
exploitable High: **any** future XSS, malicious npm dependency or browser extension yields a 7-day admin
token, and the operator cannot tell. That is why the fix belongs at the transport layer, not in the
front-end.

**Remediation.** Issue the JWT as `Set-Cookie: artex_token=…; HttpOnly; Secure; SameSite=Strict; Path=/`
from `authLogin` / `authInit`, drop `localStorage` entirely (the front-end then needs no token plumbing),
and serve over TLS (or set `Secure` conditionally on a `--tls` / trusted-proxy flag).

---

### M-2 — No HTTP security headers at all

The complete Go HTTP surface is `cors(requireAuth(mux))` plus the SPA handler:

```go
// server/server.go:1066-1069
	api := cors(s.requireAuth(mux))
	root := http.NewServeMux()
	root.Handle("/api/", api)
	root.Handle("/", s.webuiHandler())
```

A repository-wide search for `Content-Security-Policy`, `X-Frame-Options`, `X-Content-Type-Options`,
`Strict-Transport-Security`, `Referrer-Policy` and `Permissions-Policy` returns exactly **one** hit:
`server/finding_traffic.go:363`, on a single evidence-download route. `web/next.config.mjs` defines no
`headers()` block, and the static export cannot add one.

Concretely, the report export path sets a sniffable content type without `nosniff`:

```go
// server/server.go:2297-2300
	setDownload := func(contentType, filename string) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	}
```

The content of those exports (`report.FindingsMarkdown`, `report.FindingsCSV`) is derived from the LLM and
from scanned targets.

**Weakness, stated exactly.** MIME-sniffing XSS requires a browser that ignores `Content-Disposition:
attachment` and sniffs `text/csv`/`text/markdown` as HTML — modern browsers do neither. The real gap is
the missing CSP and `X-Frame-Options`: there is no defence-in-depth against any future injection on the
app origin, and the app can be framed (clickjacking the "approve this destructive tool call" prompt at
`/api/intercept/pending/{id}/decide` is exactly the kind of action a pentest operator should not be
tricked into).

**Remediation.** One middleware, before `cors`:

```go
w.Header().Set("X-Content-Type-Options", "nosniff")
w.Header().Set("X-Frame-Options", "DENY")
w.Header().Set("Referrer-Policy", "no-referrer")
w.Header().Set("Content-Security-Policy",
  "default-src 'self'; script-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
```

(Next's static export needs `'unsafe-inline'` for the theme boot script, or a nonce via a hash — compute
the hash of `theme-boot.tsx`'s emitted script at build time.)

---

### M-3 — The "safety floor" is a deletable regex set, and MCP/custom tools are outside it

The floor that remains after the RoE removal is two seeded rows; the exfiltration rule ships **disabled**:

```go
// db/db.go:540-543 (abridged)
// v1. Unlike the old guard.go floor, these are plain [Built-in] rules — the user can
// disable or delete them. The exfil rule ships DISABLED by default (its
// curl/wget/nc pipe pattern mis-fires on legitimate CTF/pentest reverse-shell and
// data-transfer pipes); enable it manually when exfil gating is actually wanted.
```

The rules are evaluated as `regexp.MatchString(string(input))` over the raw tool-input JSON
(`intercept/intercept.go:547-561`), i.e. over a *string*, so any encoding step defeats them:
`$(printf '\x72\x6d') -rf /`, `python -c "import shutil;shutil.rmtree('/')"`, `base64 -d <<<…|sh`,
`bash <<'EOF' … EOF`, or the same command split across two tool calls.

Worse, the rule system only sees seven tool names by default:

```go
// intercept/intercept.go:157-163
var defaultEnabledTools = []string{
	"Bash", "WebFetch", "web_search",
	"shell_open", "shell_send",
	"Write", "Edit", "MultiEdit",
}
```

MCP tools (`mcp__*`, created by `create_mcp`) and custom tools (`server/customtool.go`, whose `http` kind
runs `runHTTPTool` and whose `python` kind runs `execPython` → `exec.CommandContext(runCtx, interp, tmp)`
at `server/customtool.go:415`) are **never** evaluated by `applyIntercept`.

**Remediation.** (a) Add MCP and custom tools to the enabled-tool list. (b) Match on the *parsed* command
(argv after shell-word splitting), not on JSON text. (c) Treat the destructive check as code with a
hard-deny that user rules cannot delete, and keep the configurable rules as an *additional* layer.

---

### M-4 — Agent-writable persistence: `create_mcp` / `create_custom_tool` / `update_skill_file`

The platform-operations toolset is bound to the `auto` agent by default (`server/platform_tools.go:31-38`)
and every handler is registered with an unconditional allow:

```go
// server/customtool.go:305-307 (and server/orchestration.go:127-140 for the platform tools)
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
```

`create_mcp` accepts an arbitrary command line:

```go
// server/platform_tools.go:248-260
type mcpToolInput struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Transport string          `json:"transport"`
	Command   string          `json:"command"`
	Args      json.RawMessage `json:"args"`
	Env       json.RawMessage `json:"env"`
```

which is persisted and executed at every start-up:

```go
// server/mcpdiscover.go:43-47
	case "stdio":
		if m.Command == "" { return nil, errors.New(errMCPStdioNoCommand) }
		return mcp.NewStdioClient(ctx, m.Name, m.Command, jsonStrMap(m.Env), jsonStrSlice(m.Args)...)
```

`update_skill_file` similarly writes arbitrary content and paths inside the skills tree
(`server/platform_tools.go:123-165`), which is re-read into every future prompt.

**Impact.** A prompt injection that reaches the `auto` (or chat) agent gains *persistence*: the payload
survives restart, is not visible in the tool-approval UI, and is not covered by the intercept rules (M-3).
This is strictly worse than the same payload typed into Bash, which at least appears in the audit log.

**Remediation.** Route these three tools through `permission.AskUser` (they are configuration changes, not
recon), surface them in `/api/intercept/pending`, and show MCP `command`/`args`/`env` in the UI before
first launch of a newly created server.

---

### M-5 — Captured third-party credentials are stored in the clear and sent to the LLM

```go
// traffic/traffic.go:60-68
CREATE TABLE IF NOT EXISTS exchange_bodies (
  id        TEXT PRIMARY KEY,
  req_head  TEXT NOT NULL DEFAULT '',
  req_body  BLOB,
```

```go
// traffic/traffic.go:653-666
func requestHeaderLines(req *mproxy.Request) string {
	headers := req.Header.Clone()
	headers.Del("Host")
	…
	b.WriteString(headerLines(headers))
```

Nothing is filtered, so `Authorization: Bearer …`, `Cookie: session=…` and API keys from the *target*
land in `data/traffic/index.db`, and `Traffic.Get` returns the full raw head to whoever asks:

```go
// traffic/traffic.go:955-959
	err = t.db.QueryRow(`SELECT b.req_head,b.req_body,b.req_blob,b.resp_head,b.resp_body,b.resp_blob,…
	if err == nil {
		return assembleRaw(reqHead, reqBody, reqBlob, reqLen), assembleRaw(respHead, …), nil
```

`traffic_get` is a model-facing tool (`traffic/traffic.go:134` `TrafficSearchDescription`), so those
credentials are placed in the prompt and therefore leave the machine to the configured LLM provider.

**Impact.** For a product used on client engagements this is a data-handling finding: third-party session
tokens are (a) at rest unencrypted next to a *different* party's data and (b) disclosed to an LLM vendor.
Not a code bug — a design fact that must be documented and made configurable.

**Remediation.** Add a `traffic_redact_headers` setting (default: `authorization,cookie,x-api-key,set-cookie`)
applied in `requestHeaderLines`/`responseHeaderLines` and in `Traffic.Get`; store only a masked tail for
matching; document the LLM egress in `SECURITY.md`.

---

### M-6 — The first-run setup token is written to persistent, API-served logs

```go
// server/auth.go:293
	log.Printf(logT("[auth] the admin password has not been set yet. Setup token: %s"), tok)
```

`StartLogCapture` (`server/logsink.go:50`) redirects `log` through `logSinkT`, which keeps a 3 000-line
ring, fans it out to SSE subscribers and persists every line to the `server_logs` table
(`server/logsink.go:56-90`). `/api/logs`, `/api/logs/history` and `/api/logs/stream` serve it.

**How weak, and why not worse.** All three log endpoints sit behind `requireAuth`, and the token is cleared
on the first successful init (`clearSetupToken`, `server/auth.go:306`), so a remote attacker cannot read a
*live* token this way. The residual issues are real but smaller: the token is recoverable from a database
dump or container log forever afterwards, and it reaches `docker logs`, so any local user in the `docker`
group can read it during the (unauthenticated) setup window.

**Remediation.** Print it with `fmt.Fprintf(os.Stderr, …)` instead of `log.Printf`, bypassing the sink.

---

### M-7 — Unpinned remote script and tag-only base images in the build

```dockerfile
# Dockerfile:24-26  (identical pattern at Dockerfile.hardened:68-71)
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
```

`setup_22.x` is a moving target executed as root inside the build; there is no checksum and no pin. Base
images are also tag-pinned only and mutable:

* `Dockerfile:17` — `FROM python:3.12-slim-trixie`
* `Dockerfile.hardened:15-16` — `ARG NODE_IMAGE=node:22-trixie-slim`, `ARG GO_IMAGE=golang:1.26-trixie`
* `docker-compose*.yml` — `postgres:16-alpine`

**Remediation.** `FROM python:3.12-slim-trixie@sha256:<digest>` (and the same for the other four);
replace the NodeSource pipe with a distro package or a verified tarball with a pinned SHA256; add
`--network=none` where possible in build stages that do not need the network.

---

### M-8 — CI least-privilege: `contents: write` on every release job, actions on floating tags

```yaml
# .github/workflows/release.yml:5-6
permissions:
  contents: write # required to create the Release
```

That is the *workflow* level, so `frontend` and `binaries` inherit it. `frontend` runs
`pnpm install --frozen-lockfile` (`release.yml:25`), and `web/pnpm-workspace.yaml:1-4` deliberately enables
lifecycle scripts for `@parcel/watcher`, `@swc/core` and `sharp` — i.e. third-party post-install code runs
with a token that can push to the repository. Only the `docker` job narrows it (`release.yml:114-117`).

Every action in every workflow is pinned to a floating major tag (`actions/checkout@v4`,
`actions/setup-go@v5`, `pnpm/action-setup@v4`, `docker/build-push-action@v6`,
`softprops/action-gh-release@v2`, `docker/login-action@v3`, `docker/metadata-action@v5`,
`docker/setup-qemu-action@v3`, `docker/setup-buildx-action@v3`, `actions/{upload,download}-artifact@v4`).

**What is right, and worth saying:** no workflow uses `pull_request_target` or `workflow_run` (verified by
grep), and `ci.yml`, `web.yml`, `docs.yml`, `detections.yml`, `external-links.yml` all start from
`permissions: contents: read`, so untrusted PR code never gets a write token or a repository secret. This is
a least-privilege *deviation on the trusted path only*.

**Remediation.** Move `contents: write` onto the `release` job alone (`permissions: {contents: write}` at
job level) and give `frontend`/`binaries` `contents: read`; pin every `uses:` to a commit SHA.

---

### M-9 — `insecure: true` turns off TLS verification on credential-bearing MCP connections

```go
// mcphttp/client.go:100-107
// New connects to a remote MCP endpoint and performs the initialize handshake.
// headers are sent on every request (Authorization, custom API keys, …).
// When insecure is true, TLS certificate verification is skipped so servers that
// present a self-signed certificate can still be reached (issue #108).
func New(ctx context.Context, server, url string, headers map[string]string, insecure bool) (*Client, error) {
	cleanHeaders, err := normalizeHeaders(headers)
	…
	if insecure {
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
```

Same at `mcphttp/client.go:129-131` for the SSE transport. The `env` map doubles as HTTP headers
(`server/mcpdiscover.go:52-53`), so `insecure: true` puts an `Authorization` value on an unauthenticated
channel.

**Remediation.** Reject the combination `insecure == true` with a non-empty credential header set, or add
a `tls_fingerprint` (SPKI pin) field and verify against it instead of skipping verification.

---

### L-1 — JWT accepted as a query parameter on every endpoint

```go
// server/auth.go:444-453
// extractToken reads the JWT from Authorization: Bearer header,
// artex_token cookie, or ?token= query param (for SSE connections).
func extractToken(r *http.Request) string {
	…
	return r.URL.Query().Get("token")
}
```

The exemption is global, but the stated justification ("for SSE connections") only applies to
`/api/logs/stream`, `/api/update/stream`, `/api/exploration/activity/stream` and
`/api/side-questions/{id}/events`. The front-end only uses it for SSE (`web/src/lib/api.ts:201-210`).

**Remediation.** Accept the query parameter only on routes whose path ends in `/stream` or `/events`.

---

### L-2 — Dev-mode origin relaxation

```js
// web/next.config.mjs:18-19
  // In dev, allow any IPv4 origin to reach /_next/* and HMR (so a changing LAN IP does not matter).
  // Note: for security Next forbids a bare "*", so a segmented wildcard is required; "*.*.*.*" matches any IPv4.
  allowedDevOrigins: ["*.*.*.*"],
```

and the production CORS default includes dev origins:

```go
// server/server.go:4086
var defaultCORSOrigins = []string{"http://localhost:5173", "http://127.0.0.1:5173"}
```

`allowedDevOrigins` only affects `next dev`, so the production impact is nil — but a developer running
`next dev` on a LAN-exposed laptop accepts cross-origin dev/HMR requests from any IPv4 origin, and the
production default CORS list admits a local dev server that is not part of the deployed system.

**Remediation.** `allowedDevOrigins: ["localhost", "127.0.0.1"]`, and make `defaultCORSOrigins` empty (set
`ARTEX_CORS_ORIGINS` when running `next dev` from a separate origin).

---

### L-3 — Auto-update trusts an unsigned checksum delivered over the same channel

`selfupdate/github.go:23-31` and `selfupdate/stage.go:134-155` correctly require HTTPS, restrict the host
to GitHub domains, and compare the artifact against `SHA256SUMS`. But `SHA256SUMS` is fetched from the same
release as the artifact, over TLS with no signature, so the integrity guarantee is only as strong as TLS to
GitHub and the release account's security — it detects corruption and CDN tampering, not a compromised
maintainer account.

**Remediation.** Sign `SHA256SUMS` with a minisign/Sigstore key whose public half is compiled into the
binary, and verify the signature in `fetchSums` before parsing.

---

### L-4 — Incomplete HTTP server timeouts and unbounded JSON bodies

```go
// cmd/artex/main.go:117-121
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
```

`ReadHeaderTimeout` closes the classic slow-header attack, but there is no `ReadTimeout` or `IdleTimeout`,
so a slow body or many idle connections accumulate. And the default JSON decoder enforces no size cap:

```go
// server/server_mgmt.go:150
func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }
```

A number of handlers wrap it in `MaxBytesReader` (`server/assets.go:41`, `server/conversations.go:88`,
`server/task_categories.go:35`, `server/task_templates.go:42`) but most of the ~130 routes that use bare
`decode` do not.

**Remediation.** Add `ReadTimeout: 60 * time.Second`, `IdleTimeout: 120 * time.Second`, and a 1 MiB
`MaxBytesReader` inside `decode` (with the file-upload handlers already opting out).

---

### L-5 — World-readable evidence, uploads and work dir

`server/workspace.go:174` writes `0o644`, `server/workspace.go:170` / `:195` and `server/chatupload.go:98`
create `0o755`, `traffic/traffic.go:552` writes `0o644`. Given M-5 (captured credentials) and the findings'
evidence, these should not be world-readable on a shared host. gosec reports 22 × G301 and 10 × G306 for
this reason. Low severity because the default deployment is single-user and the hardened container is
root-isolated.

**Remediation.** `0o750` for directories, `0o600` for files containing captured or reported data.

---

## 4. Verified non-issues (checked, found sound, with evidence)

These are the places where the code does the right thing. They are recorded so the next reviewer does not
re-audit them, and so that the weaknesses above are read in context.

### 4.1 Auth and session

* **`requireAuth` covers every `/api/` route.** The complete mux is wrapped once:
  `api := cors(s.requireAuth(mux)); root.Handle("/api/", api)` (`server/server.go:1066-1069`), and the only
  exemptions are an explicit prefix and one path:
  `if strings.HasPrefix(p, "/api/auth/") || p == "/api/health"` (`server/auth.go:461`). The four exempt
  handlers are `status` (read-only install state), `init` (setup-token gated), `login`, and
  `change-password` — and the last one re-validates the token itself before doing anything
  (`server/auth.go:590-593`). **No accidentally-unauthenticated route found.** *(Deliberately public:
  `/api/auth/*`, `/api/health`, and the SPA handler `root.Handle("/", s.webuiHandler())`, whose comment at
  `server/server.go:1064-1065` documents that the UI is public and the API is the boundary. That is
  consistent: the SPA contains no data.)*
* **No JWT algorithm confusion, no `alg:none`.** The key function rejects anything that is not HMAC before
  returning the key: `if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok { return nil, … }`
  (`server/auth.go:130-136`).
* **No username oracle.** Login always runs bcrypt even for a wrong username:
  `// Always run the bcrypt comparison so a wrong username costs the same as a wrong password`
  (`server/auth.go:687-691`).
* **The setup token is compared in constant time and cannot be empty-matched.**
  `subtle.ConstantTimeCompare` with an explicit empty guard (`server/auth.go:300-304`).
* **The one-shot setup race is closed at the database, not in code.**
  `InsertSettingIfAbsent` + a `403` on `!inserted` (`server/auth.go:562-577`), and the same guarantee for
  the env-var path (`server/auth.go:256-264`).
* **bcrypt cost is the library default (10)** — `bcrypt.DefaultCost` at `server/auth.go:254, 555, 639,
  689`. Adequate for a fast interactive login; the 72-byte and 8-character bounds are enforced server-side
  (`server/auth.go:544-553`).
* **Login rate limiting is keyed on the TCP peer, not on `X-Forwarded-For`.** The reasoning is written
  down and correct (`server/auth.go:395-410`, `clientKey`), the limiter is bounded (10 000 entries,
  `authLimiterMaxItems`) and cooldowns double to a 10-minute cap.
* **Password change revokes all sessions, by construction.** `signingKey` derives the HMAC key from the
  stored bcrypt hash (`server/auth.go:147-157`), so any password change — API, `reset-password.sh`, or a
  direct DB edit — invalidates every issued token, and a 2-second hash cache bounds staleness
  (`passHashCacheTTL`, `server/auth.go:175`).
* **A database outage cannot be mistaken for "no password set".** `readPassHash` returns the error
  separately and `authStatus` answers `503` rather than `initialized:false` (`server/auth.go:481-492`),
  with the reasoning in the comment.
* **CORS is an explicit allowlist, not `*`.** `server/server.go:4111-4127`, and the comment at 4103-4106
  records the vulnerability that motivated it.
* **CSRF is not practically exploitable**: the cookie is `SameSite=Lax`, CORS is allowlisted, and every
  mutating handler decodes JSON (`Content-Type: application/json` forces a preflight).

### 4.2 Archives and path handling

* **Zip-slip in skill upload is properly closed.** `skillRelPath` (`server/server_mgmt.go:1582-1614`)
  rejects `..`, leading `/`, empty segments, `\`, `%`, control/format characters and bidi overrides *before*
  normalising, then `path.Clean` + `path.IsAbs` + `..`-prefix re-check, and the extraction target is
  re-joined and re-checked with `filepath.Join(tmp, clean)`. Bounded by 20 MiB request /
  20 MiB per file / 100 MiB total / 4 000 entries (`server/server_mgmt.go:1332-1335`), with the actual
  `io.Copy` additionally limited to `maxSkillFileBytes+1` so a lying header cannot exceed it
  (`server/server_mgmt.go:1489`). Extraction happens into a temp dir and is swapped in with a single
  `os.Rename`.
* **Task-archive (tar.zst) extraction is sound.** Path containment is checked twice
  (`server/task_archive_package.go:700-708`), **symlinks and hardlinks are rejected** because only
  `tar.TypeDir`, `tar.TypeReg` and `tar.TypeRegA` are accepted and everything else returns
  `"unsupported task archive entry type"` (`server/task_archive_package.go:726-728`), and extraction is
  bounded by `maxArchiveFiles` / `maxArchiveBytes` (`server/task_archive_package.go:695-699`) with a
  mandatory SHA-256 check (`server/task_archive_package.go:664-680`). `O_CREATE|O_EXCL` also stops
  overwriting through a pre-existing path.
* **Chat upload filename sanitisation is correct.** `filepath.Base(hdr.Filename)` (`server/chatupload.go:114`)
  followed by explicit rejection of `""`, `.`, `..` and any remaining `/` or `\`
  (`server/chatupload.go:115-117`), and the `{id}` segment is constrained by
  `safeChatID = ^[A-Za-z0-9_-]+$` (`server/chatupload.go:37`).
* **Workspace `..` traversal is closed.** `wsResolve` collapses `..` on a rooted copy and re-checks the
  prefix (`server/workspace.go:44-59`) — the *lexical* half is right; only the symlink half (H-4) is
  missing.
* **Evidence blob paths cannot traverse.** The hash must be exactly 64 lowercase hex characters before it
  is joined into a path (`evidence/store.go:33-45`) and blobs are re-verified by SHA-256 and length on read
  (`evidence/store.go:43-62`).

### 4.3 SQL

**All queries are parameterised.** gosec's 13 × G202 "SQL string concatenation" hits were each read; every
one concatenates a *placeholder list*, never a value:

```go
// db/notification.go:341
			vals = append(vals, fmt.Sprintf("($%d,$%d)", len(args)+1, len(args)+2))
// db/notification_delivery.go:219
		ph, idArgs := placeholders(2, ids)
// traffic/archive.go:60-64
		placeholders[i] = "?"
```

The only identifier interpolations are guarded by allowlists:

```go
// db/task_archives_restore.go:387-401
	allowed := map[string]bool{ "exploration_nodes": true, …, "side_question_requests": true }
	if !allowed[table] { return fmt.Errorf("archive restore table %q is not allowed", table) }
	_, err := tx.Exec(`INSERT INTO `+table+` SELECT * FROM json_populate_recordset(NULL::`+table+`,$1::json)`, string(raw))
```

and the streamed variant hard-codes `llm_records` (`db/task_archives_restore.go:403-407`). `rowExists` uses
an explicit five-name check (`db/task_archives_restore.go:355-360`). **No string-built SQL with
user-controlled input found.**

### 4.4 SSRF and outbound requests

* `notify/http.go` is the strongest control in the repository, and it is enforced at dial time so DNS
  rebinding and same-host redirects cannot defeat it:

```go
// notify/http.go:75-100
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() { return nil }
	host, _, err := net.SplitHostPort(address)
	…
	ip := net.ParseIP(host)
	if ip == nil { return fmt.Errorf("could not resolve the target address: %q", host) }
	if isBlockedDialIP(ip) { … }
```

  plus IPv4-mapped-IPv6 normalisation before the range check (`notify/http.go:54-66`), a cross-host
  redirect refusal motivated by vendor tokens living in the URL (`notify/http.go:126-134`), an 8 KiB
  response cap (`notify/http.go:136-137`), and URL redaction on every error path
  (`notify/http.go:231-286`). Deliberate, documented gap: RFC1918 is *not* blocked
  (`notify/http.go:44-53`) — acceptable and clearly stated.
* `selfupdate` pins `https` + a five-host allowlist on **every** redirect hop:
  `checkURL` (`selfupdate/github.go:82-93`) and `CheckRedirect` (`selfupdate/github.go:73-80`), then
  verifies SHA-256 (`selfupdate/stage.go:93-97`), smoke-tests the new binary
  (`selfupdate/stage.go:101-103`), records the staged file's own digest and re-verifies it before the swap
  (`selfupdate/stage.go:105-110`, `selfupdate/bootstrap.go:169-180`).
* `mcphttp`'s `insecure` flag is an explicit per-server admin opt-in with a documented reason (issue #108)
  — see M-9 for the residual credential exposure.
* `enrich/enrich.go:96` and `traffic/traffic.go:206` set `InsecureSkipVerify`/`SslInsecure` **on purpose**:
  these are the pentest probe and the MITM recorder, whose job is to talk to self-signed targets.

### 4.5 Secrets, logging and notifications

* Notification credentials are never echoed: `MaskConfig`/`MaskedValue` keep only the last 6 characters
  (`notify/mask.go:27-36`), and `PrepareConfigUpdate` refuses the silent "change the URL, keep the
  credentials" primitive with an explicit error (`notify/mask.go:86-160`).
* `llm_profiles.api_key` is `json:"-"` so it cannot be serialised to the UI; only a 4-character hint is
  exposed (`db/config.go:21-22`, `db/config.go:163-165`).
* `llmrec` raw capture stores the request **body** only (`llmrec/capture.go:53-67`) — no
  `Authorization` headers — and only while recording is enabled.
* No `log.Printf` call in the repository passes a credential; the grep for
  `log.Printf.*\(key|token|password|secret|authorization\)` returns only env-var *names* and file *paths*
  (`server/auth.go:88, 103, 251-266, 284`).

### 4.6 Container and CI (beyond the findings)

* `docker-compose.hardened.yml` is a genuinely good reference: non-root UID 10001, `read_only: true`,
  `cap_drop: [ALL]`, `no-new-privileges`, `init: true`, `pids_limit`, `ulimits`, an `internal: true`
  Postgres network with no egress, `8788` unpublished, and `tmpfs` mounted `nosuid,nodev`
  (`docker-compose.hardened.yml:57-148`).
* `Dockerfile.hardened` strips setuid bits and installs the binary `0555` (`Dockerfile.hardened:84-101`).
* `ci.yml`, `web.yml`, `docs.yml`, `detections.yml`, `external-links.yml` all set
  `permissions: contents: read` at workflow level, and none uses `pull_request_target` or
  `workflow_run`, so PR code never sees a write token.
* The static export (`web/out`) is git-ignored (`web/.gitignore:14`) and not committed; the only
  `NEXT_PUBLIC_*` variables are `LOCALE`, `SSE_BASE` and `MOCK` — none is a secret.
* `web/src/proxy.ts` and `web/src/lib/auth.ts` are explicitly a *navigation* guard, not an authorization
  boundary (`// Not logged in → redirect to the login page`, `web/src/proxy.ts:15-17`); the API is the
  boundary, and that is the correct split.

### 4.7 Scanner hits confirmed as false positives

| Rule | Hits | Verdict |
|---|---|---|
| G101 "hardcoded credential" | 6 | All are env-var name constants (`"ARTEX_SETUP_TOKEN"`, `"ARTEX_ADMIN_PASSWORD"`) and user-facing message strings containing the words "API Key". Read at `server/auth.go:33,36,57`, `server/server.go:100`, `server/server_mgmt.go:123`, `server/task_resolution.go:27`. No secret. |
| G708 SSTI | 1 | `server/server_mgmt.go:2247` renders an admin-authored prompt template with `text/template` and no `FuncMap`, into a JSON preview, after checking every `{{.Var}}` against a catalog allowlist (`validateTemplate`, `server/server_mgmt.go:2228-2241`). Not an RCE path. |
| G202 SQL concat | 13 | Placeholder lists only — see §4.3. |
| G703 path traversal | 24 | Each one read. Guarded by `skillRelPath` (`server/server_mgmt.go:1672-1682`), `validSkillName` (10 call sites) or `wsResolve` (`server/workspace.go:44`); the two in `server/auth.go:85-86` use the executable's own base dir; `config/config.go:192` uses `ARTEX_SKILL_DIR`/config-file paths, not request data. The *symlink* half of that guarantee is H-4. |
| G304 file inclusion | 43 | Same set as G703, plus evidence blobs (hash-validated) and self-update paths (release-metadata-derived). |
| G120 unbounded multipart | 2 | Both bounded: `http.MaxBytesReader` 128 MiB (`server/chatupload.go:102`) + `ParseMultipartForm(32<<20)` (`server/chatupload.go:103`), 512 MiB + 32 MiB (`server/workspace.go:254-255`). |
| G110 decompression bomb | 1 | `server/finding_traffic_export.go:115` re-reads a **ZIP the server itself just wrote** to validate CRCs. Not attacker-supplied input. |
| G122 Walk symlink TOCTOU | 4 | All four walk directories the server created (`evidence/blobs`, archive staging, `traffic/`), not attacker-planted trees. |
| G115 integer overflow | 3 | `fs.FileMode(header.Mode)&0o777` (`server/task_archive_package.go:717`), a rune conversion in `enrich` and a size conversion in `llmpool`; no security impact. |

---

## 5. Residual risk & follow-ups

1. **`GO-2026-5932` (x/crypto/openpgp) is a module-only finding — confirmed not reachable.**
   `govulncheck` reports 0 symbol-level and 0 package-level vulnerabilities; the advisory appears only under
   "Module Results" because `golang.org/x/crypto@v0.58.0` is in the module graph
   (via `projectdiscovery/*` → `x/crypto`), and nothing in this repository imports
   `golang.org/x/crypto/openpgp`. No action needed beyond the eventual `x/crypto@v0.58.1-…` bump.
2. **`pnpm audit`: 0 high, 0 critical** — see the appendix. Both advisories reach the tree through
   `shadcn` (a build-time CLI in `dependencies`, not a browser bundle): `deepmerge` (low,
   prototype poisoning) and `minimatch` (moderate, ReDoS). Neither ships to users; note that they are
   *not* dev-only in pnpm's classification because `shadcn` sits in `dependencies`, so the "ignore dev-only"
   rule does not filter them — they are recorded and accepted as build-tool-only.
3. **Unaudited by design / out of reach of this review.** No dynamic testing was performed (explicitly out
   of scope, and the test suite was being repaired concurrently). Runtime behaviour of the LLM provider
   integration (`llmpool`, `llmretry`) and the Playwright MCP browser were only read, not exercised.
4. **The largest residual risk is H-1 + H-2 together**, and it is structural: the product's purpose is to
   run attacker-adjacent, model-chosen commands, and today nothing at the syscall layer bounds where those
   commands go. Everything else in this report is comparatively cheap to fix.
5. **Follow-up candidates not raised as findings** (low value, or already consciously accepted):
   `/api/health` discloses `BuildVersion` unauthenticated (fingerprinting); JWT has no `iss`/`aud` and the
   `Subject` set at signing (`server/auth.go:123`) is never verified; `uniqueUploadPath`
   (`server/chatupload.go:143-158`) has a benign TOCTOU; `POST /api/tasks` and a handful of other handlers
   echo `err.Error()` to the client.

### Top 3 fixes, in order

1. **Make the hardened container the default (H-2, H-3).** Three lines in `Dockerfile`
   (`USER 10001:10001`, `-proxy 127.0.0.1:8788`) and five in `docker-compose.yml`
   (`user`, `read_only`, `cap_drop: [ALL]`, `security_opt`, named volumes). No code change, and it removes
   the root-escalation amplifier for every other finding in this report.
2. **Resolve symlinks in the two file-manager roots (H-4).** `filepath.EvalSymlinks` + re-check in
   `wsResolve` and one helper shared by the skill browser — or migrate both to `os.Root`. This is the only
   finding that lets an authenticated API read/write files the design says it cannot reach, including
   `jwt.key`.
3. **Put the session token in an `HttpOnly; Secure; SameSite=Strict` cookie and add the security-header
   middleware (M-1, M-2).** Two small changes in `server/auth.go` and one middleware in
   `server/server.go` that closes clickjacking on the tool-approval screen and caps the blast radius of
   any future XSS. Ship it together with the `?token=`-only-on-SSE restriction (L-1), since both touch
   `extractToken`.

---

## Appendix A — govulncheck (verbatim)

Command: `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` (exit 0)

```text
=== Symbol Results ===

No vulnerabilities found.

Your code is affected by 0 vulnerabilities.
This scan also found 0 vulnerabilities in packages you import and 1
vulnerability in modules you require, but your code doesn't appear to call these
vulnerabilities.
Use '-show verbose' for more details.
```

With `-show verbose`, the reachability verdict for the one residual:

```text
=== Symbol Results ===

No vulnerabilities found.

=== Package Results ===

No other vulnerabilities found.

=== Module Results ===

Vulnerability #1: GO-2026-5932
    The golang.org/x/crypto/openpgp package is unmaintained, unsafe by design,
    and has known security issues
  More info: https://pkg.go.dev/vuln/GO-2026-5932
  Module: golang.org/x/crypto
    Found in: golang.org/x/crypto@v0.58.0
    Fixed in: golang.org/x/crypto@v0.58.1-0.20261009181718-2f1f834330b2

Your code is affected by 0 vulnerabilities.
This scan also found 0 vulnerabilities in packages you import and 1
vulnerability in modules you require, but your code doesn't appear to call these
vulnerabilities.
```

Scan coverage: 17 root packages, 48 modules, plus the `go1.27.3` standard library.

**Verdict:** `GO-2026-5932` is **not reachable**. It is listed only under *Module Results*, with empty
*Package Results* and *Symbol Results* — meaning no package this code imports exposes the vulnerable API
and no symbol is called. The module arrives transitively via `projectdiscovery/*`; `golang.org/x/crypto/openpgp`
is not imported anywhere in this repository (verified by `grep -rn "crypto/openpgp" --include=*.go .` → no
matches).

---

## Appendix B — gosec summary

Command: `go run github.com/securego/gosec/v2/cmd/gosec@latest -fmt=json -out=/tmp/pi/sec/gosec.json ./...`
(gosec `dev`; exit 1 = issues found). Raw report: `/tmp/pi/sec/gosec.json`.

| Severity | Rule | Meaning | Count | Example locations |
|---|---|---|---|---|
| HIGH | G101 | Hardcoded credential pattern | 6 | server/auth.go:33, server/auth.go:36, server/auth.go:57, server/server.go:100 |
| HIGH | G115 | Integer overflow conversion | 3 | enrich/enrich.go:166, llmpool/pool.go:198, server/task_archive_package.go:717 |
| HIGH | G122 | Symlink/TOCTOU in filepath.Walk | 4 | evidence/store.go:396, server/task_archive_package.go:616, traffic/traffic.go:1839, traffic/traffic.go:1883 |
| HIGH | G402 | TLS InsecureSkipVerify | 3 | enrich/enrich.go:96, mcphttp/client.go:106, mcphttp/client.go:131 |
| HIGH | G703 | Path traversal (taint analysis) | 24 | config/config.go:192, server/auth.go:86, server/chatupload.go:133, server/chatupload.go:140 |
| HIGH | G708 | SSTI (text/template) | 1 | server/server_mgmt.go:2247 |
| MEDIUM | G110 | Decompression bomb | 1 | server/finding_traffic_export.go:115 |
| MEDIUM | G120 | Unbounded form parsing | 2 | server/chatupload.go:103, server/workspace.go:255 |
| MEDIUM | G202 | SQL string concatenation | 13 | db/notification.go:344, db/notification.go:356, db/notification_delivery.go:220-221, db/notification_delivery.go:248-250 |
| MEDIUM | G204 | Subprocess with variable | 1 | server/customtool.go:415 |
| MEDIUM | G301 | Directory perms looser than 0750 | 22 | agent/chat.go:115, agent/worker.go:282, config/config.go:192, server/chatupload.go:98 |
| MEDIUM | G302 | File perms looser than 0600 | 2 | selfupdate/bootstrap.go:188, selfupdate/stage.go:270 |
| MEDIUM | G304 | File inclusion via variable | 43 | evidence/store.go:261, evidence/store.go:291, evidence/store.go:331, evidence/store.go:44 |
| MEDIUM | G306 | WriteFile perms looser than 0600 | 10 | selfupdate/selfupdate.go:105, selfupdate/stage.go:112, server/platform_tools.go:115, server/platform_tools.go:153 |
| MEDIUM | G705 | XSS via taint analysis | 3 | server/server.go:2305, server/server.go:2322, server/side_questions.go:587 |
| LOW | G104 | Unhandled error | 61 | agent/goals.go:170-181, db/config.go:305, db/config.go:318, db/config.go:323 |
| LOW | G706 | Log injection | 55 | server/auth.go:539, server/auth.go:581, server/auth.go:635, server/auth.go:655 |

Totals as reported by gosec: **254 issues over 178 files / 66 094 lines, `nosec` directives: 0**
(HIGH 41 · MEDIUM 97 · LOW 116).

Triaged outcome: **1 real finding** (G301/G306 → **L-5**), **1 corroborating finding** (G705 → **M-2**,
partially; `server/side_questions.go:587` writes SSE frames with a JSON-encoded payload and is not
exploitable), and the rest confirmed false positives — see §4.7 for the per-rule reasoning. No `nosec`
annotations exist in the codebase, so nothing was suppressed by the authors.

---

## Appendix C — pnpm audit summary

Command: `cd web && pnpm audit --json` (exit 1 = advisories found). Raw report:
`/tmp/pi/sec/pnpm-audit.json`.

```json
"metadata": {
  "vulnerabilities": { "info": 0, "low": 1, "moderate": 1, "high": 0, "critical": 0 },
  "dependencies": 590,
  "devDependencies": 185,
  "optionalDependencies": 135,
  "totalDependencies": 850
}
```

| Advisory | Severity | Package | Version | Path | Patched |
|---|---|---|---|---|---|
| GHSA-gcqx-w48m-w2h4 (CWE-1321) | low | `deepmerge` | 4.3.1 | `.>shadcn>deepmerge`, `.>shadcn>@shadcn/registry>deepmerge` | none published |
| GHSA-g3hv-75wg-m249 (CWE-405) | moderate | `minimatch` | 10.2.6 | `.>shadcn>ts-morph>@ts-morph/common>minimatch` | `>=11.0.1` |

**High/critical: none.**

Both advisories are reached only through `shadcn` (a project-scaffolding CLI). It is declared in
`dependencies` rather than `devDependencies` (`web/package.json`), so pnpm classifies its subtree as
`dev: false` — the "ignore dev-only advisories" filter therefore does **not** remove them — but `shadcn` is
not imported by application code and does not appear in the static export in `web/out`, so neither
advisory is reachable at runtime. Recommendation: move `shadcn` to `devDependencies` (which makes the
classification honest), and accept the two advisories as build-tool-only until the upstream pins update.
