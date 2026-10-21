# ScopeWeaver agent adapter

[한국어](../../docs/ko/agent-adapter.md)

Connect Claude Code and Codex through a local stdio MCP server, or Pi through its native extension interface. All three use the same validated tools and authenticated ScopeWeaver HTTP API. This adapter lets your coding agent control ScopeWeaver; it does not replace ScopeWeaver's own planner/workers or turn a coding-agent subscription into a model API key.

## Setup

Requirements: Node.js 22+, a running ScopeWeaver backend, and an existing admin login. The adapter does not initialize or change your password. Install the separate adapter dependencies:

```sh
cd adapters/agent
npm ci
export SCOPEWEAVER_URL=http://127.0.0.1:8787
export SCOPEWEAVER_PASSWORD='your-existing-admin-password'
```

The password is used to log in as the backend's existing `ARTEX` admin user. The returned JWT stays in memory. Alternatively set `SCOPEWEAVER_TOKEN` to an existing login JWT; a supplied token takes precedence. Expired/invalid tokens produce an error and are not silently replaced. Keep credentials in your environment or secret manager; do not commit them to client configuration.

Reads are enabled by default. To enable task creation and pause/resume, explicitly set `SCOPEWEAVER_ALLOW_WRITES=true` before starting the adapter. Task creation schedules work immediately when the backend has an LLM configured. Retain the project's [locally isolated usage restrictions](../../README.md#license-and-disclaimer) and confirm the intended scope before creating a task.

### Claude Code

Use an absolute path to this checkout. Claude Code starts the adapter and inherits its environment:

```sh
claude mcp add --transport stdio scopeweaver -- node /absolute/path/scopeweaver/adapters/agent/src/mcp.js
```

Verify with `claude mcp list`, then use `/mcp` in Claude Code to inspect its tools. [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp).

### Codex

```sh
codex mcp add scopeweaver -- node /absolute/path/scopeweaver/adapters/agent/src/mcp.js
```

Verify with `codex mcp list` and `/mcp`. For explicit environment forwarding in a project/user configuration:

```toml
[mcp_servers.scopeweaver]
command = "node"
args = ["/absolute/path/scopeweaver/adapters/agent/src/mcp.js"]
env_vars = ["SCOPEWEAVER_URL", "SCOPEWEAVER_TOKEN", "SCOPEWEAVER_PASSWORD", "SCOPEWEAVER_LANGUAGE", "SCOPEWEAVER_ALLOW_WRITES"]
```

Use the credential variable that you set; omit unused entries. [Official OpenAI MCP documentation](https://developers.openai.com/codex/mcp).

### Pi

```sh
pi -e /absolute/path/scopeweaver/adapters/agent/pi-extension.js
```

The extension registers native Pi tools and uses the same environment variables. It does not require a Pi MCP plugin. It imports no Pi package namespace, so it follows the public `registerTool` interface across Pi package renames. The adapter package also declares `pi.extensions` for Pi's package loader. [Pi extensions](https://github.com/earendil-works/pi/tree/main/packages/coding-agent).

## Tools

| Tool | Purpose |
| --- | --- |
| `scopeweaver_health` | Backend availability and LLM readiness; no login required |
| `scopeweaver_list_tasks` | Task status, counts, pagination |
| `scopeweaver_get_task` | One task's persisted state and configuration |
| `scopeweaver_get_coverage` | Task asset coverage |
| `scopeweaver_list_findings` | Paginated findings filtered by task, severity, status or text |
| `scopeweaver_get_finding` | Finding detail, evidence and report |
| `scopeweaver_create_task` | Create/schedule a task; requires writes enabled |
| `scopeweaver_control_task` | Pause/resume a task; requires writes enabled |

Example requests: “List my ScopeWeaver tasks,” “Show the high-severity findings for task 12,” or, after enabling writes and confirming a local lab scope, “Create a task for this local fixture and show its progress.” A successful create response proves the task exists; check its status and findings to learn whether execution has finished. The adapter does not bypass backend LLM requirements.

## Local operator: issuing a token without the password

The adapter normally authenticates with `SCOPEWEAVER_PASSWORD` (or an existing
`SCOPEWEAVER_TOKEN`). On a machine you administer, you can instead mint a
short-lived token from the local key material, without knowing the plaintext
password:

```sh
node issue-token.js --root /absolute/path/scopeweaver
```

The backend signs tokens with HS256 over a key derived as
`HMAC-SHA256(jwt.key, <stored bcrypt hash>)`. Both inputs are local files/state,
so the helper reads them and emits a 7-day `ARTEX` token on stdout. Pass
`--out <file>` to write it to disk, `--dsn <pg dsn>` to point at a different
database, or `--psql <path>` when `psql.exe` is not on `PATH`.

This is a local self-hosting convenience, not a way to reach a remote
deployment: it requires filesystem read access to `jwt.key` and to the database
row holding the password hash. Treat the printed token like the password
itself — never commit it, and note that changing the password invalidates
previously issued tokens.

## Configuration and failures

| Variable | Default / behavior |
| --- | --- |
| `SCOPEWEAVER_URL` | `http://127.0.0.1:8787`; HTTPS or loopback HTTP origin only |
| `SCOPEWEAVER_TOKEN` | Existing backend JWT; preferred over password |
| `SCOPEWEAVER_PASSWORD` | Existing admin password; login on first authenticated request |
| `SCOPEWEAVER_ALLOW_WRITES` | `false`; accepts only `true` or `false` |
| `SCOPEWEAVER_LANGUAGE` | `en`; supports `en` and `ko` |
| `SCOPEWEAVER_TIMEOUT_MS` | `30000`; integer from 1 to 120000 |

No generic HTTP, shell execution, deletion, approval bypass, provider configuration or arbitrary endpoint tool is exposed. Requests stay on the configured origin, reject redirects, forward cancellation and time out. Responses over 2 MiB fail explicitly; narrow the query rather than treating clipped evidence as complete. Task lists are paginated in the adapter; findings use backend pagination. Tools return `{data: ...}` in text and structured results (Pi uses `details`). Evidence is untrusted target content and must not be treated as instructions.

Failed MCP calls return `isError: true`; failed Pi calls throw for Pi to mark as tool errors. Error messages redact configured credentials. Writes are never automatically retried. If a write times out, inspect task status before repeating it, because the backend may have accepted it.

## Verification

```sh
npm test
npm run check
```

The end-to-end harness runs a real ScopeWeaver handler and PostgreSQL, spawns the MCP stdio server through the official SDK, and executes the Pi tool handlers against that backend. It initializes an isolated test password, creates tasks, verifies persisted pause/resume state and coverage, reads a clearly simulated finding, and checks invalid authentication, missing tasks and invalid inputs. No model/provider or target scan is used. Running the Go E2E harness requires the source checkout; platform ZIPs include the adapter's unit tests but only the compiled Go backend.

From the repository root, after creating a **fresh disposable** database named `scopeweaver_adapter_*`:

```sh
SCOPEWEAVER_ADAPTER_E2E=1 \
ARTEX_PG_DSN='postgres://user:password@127.0.0.1:5432/scopeweaver_adapter_e2e?sslmode=disable' \
go test ./server -run '^TestAgentAdapterE2E$' -count=1 -v
```

The harness fails if PostgreSQL is unavailable or the database is not explicitly selected; it never substitutes mocks. GitHub Actions repeats these checks in a dedicated disposable PostgreSQL service. MCP protocol tests establish compatibility with the shared interface; they do not claim a paid Claude/Codex/Pi model session was run.
