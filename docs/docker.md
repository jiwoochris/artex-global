# Running ARTEX with Docker

One stack, two services (`artex` + `postgres`), secure by default and usable with a
single `docker compose up -d`. This document is both the setup guide and the record of
why every isolation setting is there; if you change `docker-compose.yml`, keep
`deploy/posture_test.go` green (CI runs it).

| File | What it is |
| --- | --- |
| `docker-compose.yml` | The only stack: published image by default, `docker compose build` for a source build |
| `docker-compose.volumes.yml` | Opt-in overlay: named volumes instead of host bind mounts (no `chown`) |
| `.env.example` | Every setting the stack reads; copy to `.env` |
| `Dockerfile` | Run-only image: pre-compiled binary + operational tooling, non-root user |

There is deliberately no second "hardened" stack any more. The posture that used to live
in a separate file is the default here, because the useful parts of it cost nothing to
run: only three things are genuinely exclusive (read-only rootfs, named volumes, no
`NET_RAW`), and each is an opt-in documented in §7, §6 and §9.

## 1. What the isolation does

| Measure | Effect |
| --- | --- |
| `user: "10001:10001"` (user `artex`, no login shell) | Nothing runs as root, not even commands the agent launches. |
| `cap_drop: [ALL]` + `cap_add: [NET_RAW]` | The container holds exactly one capability: the one raw-socket port scanning needs. No `SYS_ADMIN`, no `NET_ADMIN`, no `DAC_OVERRIDE`. |
| `security_opt: no-new-privileges` | A setuid binary or a file capability cannot elevate anything inside the container. |
| `init: true` | tini reaps the processes the agent spawns, so no zombie accumulation over a long run. |
| `tmpfs /tmp` (2 GB, `nosuid`, `nodev`) | Working space in RAM for downloaded tooling; a runaway tool fills RAM, not the host disk. |
| Bridge `app_net` | Outbound internet is allowed (LLM APIs, scan targets). There is **no** `network_mode: host`. |
| Internal `db_net` | PostgreSQL has no route to the internet or to your LAN; only `artex` can reach it. |
| No Docker socket, no extra host mounts | The agent cannot start containers, read `~/.ssh`, or mount anything you did not declare. Only `./data` and `./skills` are visible. |
| `pids_limit` (4096 by default) | A fork bomb or a runaway browser cannot exhaust the host's PID table. |
| `ulimits` (`core: 0`, `nofile` 8192/16384) | No core dumps on the host; enough descriptors for wide scans. |
| `logging` rotation (20 MB × 3) | Logs cannot fill the host disk. |
| Only loopback ports published | UI/API and the recording proxy listen on `127.0.0.1`; nothing is exposed to the local network by default. |
| Healthcheck + `stop_grace_period: 60s` | `docker stop` waits for running work instead of killing it mid-task. |

What is **not** locked down, on purpose: the container's root filesystem stays writable,
because the UI's one-click self-update replaces the binary in place. That is the only
deliberate relaxation; §7 has the read-only overlay for deployments that upgrade by
pulling a new image.

### Verify it yourself

```bash
# unprivileged user → uid=10001(artex) gid=10001(artex)
docker compose exec artex id

# no-new-privileges, capability drop/add, no host network, tmpfs
docker inspect "$(docker compose ps -q artex)" \
  --format 'user={{.Config.User}} caps={{.HostConfig.CapAdd}} drop={{.HostConfig.CapDrop}} sec={{.HostConfig.SecurityOpt}} net={{.HostConfig.NetworkMode}}'

# raw-socket capability (prints "raw socket: OK" or PermissionError)
docker compose exec artex python3 -c \
  "import socket; socket.socket(socket.AF_INET, socket.SOCK_RAW, socket.IPPROTO_TCP); print('raw socket: OK')"

# ICMP works through ping_group_range even without the capability
docker compose exec artex ping -c1 127.0.0.1

# internet egress works (200); the LAN is reachable unless you apply §8
docker compose exec artex curl -fsS -o /dev/null -w '%{http_code}\n' https://openrouter.ai/api/v1/models

# postgres is NOT reachable from outside the compose networks
docker compose port postgres 5432 || echo "postgres has no published port (expected)"
```

`NET_RAW` is granted to the container, but a non-root user can only use it if the
runtime keeps the capability effective across `execve` (runtimes differ; the raw-socket
probe above answers it in one line). When it is not effective, connect-scan (`-sT`)
still works and only SYN/OS-detection scans are lost — see §9.

## 2. Requirements

- Docker Engine 24+ with the Compose v2 plugin (`docker compose version`).
- Roughly 3 GB of disk for the image (it ships Chromium) plus whatever the targets'
  traffic occupies.
- Outbound HTTPS for the LLM provider and for the targets you are authorized to test.
- Ports free on the host: `8787` (UI/API) and `8788` (recording proxy).

## 3. Startup

```bash
git clone https://github.com/yukiteruamano/artex-global.git
cd artex-global
cp .env.example .env        # set POSTGRES_PASSWORD; an LLM key is optional here
mkdir -p data skills        # must exist before the first start (see §5)
docker compose up -d
docker compose logs -f artex              # first start prints the setup token
```

By default the container runs as uid/gid `10001`, so `./data` and `./skills` have to be
writable by it. On a single-user host the simplest way to get that without touching
permissions is to run the container as yourself:

```bash
ARTEX_UID=$(id -u) ARTEX_GID=$(id -g) docker compose up -d   # or put both lines in .env
```

Open `http://127.0.0.1:8787`, complete `/setup` with the token from the log (or skip it by
setting `ARTEX_ADMIN_PASSWORD` in `.env`), then configure the LLM in **System → LLM**.

Other entry points:

```bash
docker compose up -d --build                                  # build from source instead
docker compose -f docker-compose.yml -f docker-compose.volumes.yml up -d   # named volumes
docker compose -f docker-compose.yml -f docker-compose.readonly.yml up -d # read-only rootfs
```

`ARTEX_BIND=0.0.0.0` in `.env` publishes the UI on all host interfaces (finish the setup
first, and prefer the reverse-proxy layout in the README). `ARTEX_PROXY_ADDR=0.0.0.0:8788`
does the same for the recording proxy — do that only on a host you control, because the
proxy has no authentication.

## 4. API keys

ARTEX speaks two formats: **`openai`** (Chat Completions, any compatible gateway)
and **`anthropic`** (Messages). OpenRouter and OpenCode Go go through the former.
Configuration can be done through `.env` (a single profile, no extra headers) or in
the UI (**System → LLM → New**), which is the complete path.

### 4.1 OpenRouter (through `.env`, the shortest way)

ARTEX uses the `OPENAI_API_KEY` variable for any OpenAI-compatible endpoint, so the
OpenRouter key goes there:

```dotenv
OPENAI_API_KEY=sk-or-v1-...
ARTEX_LLM_PROVIDER=openai
ARTEX_LLM_BASE_URL=https://openrouter.ai/api/v1
ARTEX_LLM_MODEL=anthropic/claude-sonnet-4.5   # the exact slug from openrouter.ai/models
```

```bash
docker compose up -d
```

### 4.2 OpenRouter (through the UI)

**System → LLM → New**:

| Field | Value |
| --- | --- |
| Format | `OpenAI` |
| Base URL (optional) | `https://openrouter.ai/api/v1` |
| Model | the slug, e.g. `anthropic/claude-sonnet-4.5` |
| API Key | `sk-or-v1-...` |
| Context window (K) | the real window of the model (0 = 200K by default) |
| Output cap · max tokens | generous if the model reasons (e.g. `16000`) |

Click **Test connection** and then **Set active** on the profile card.

### 4.3 OpenCode Go (through the UI — mandatory)

The *Go* plan requires the `x-opencode-session` header on every request; without it
the gateway answers 400. That header can only be set in a UI profile, not through
environment variables:

- Base URL of the Go plan: `https://opencode.ai/zen/go/v1`
- (Zen, pay per use: `https://opencode.ai/zen/v1` — it does not require the header)

**System → LLM → New**:

| Field | Value |
| --- | --- |
| Name | `OpenCode Go` |
| Format | `OpenAI` |
| Base URL (optional) | `https://opencode.ai/zen/go/v1` |
| Model | an id from the catalog, e.g. `glm-5.3` |
| API Key | your `oc_sk_...` key |
| Custom session header (optional) | `x-opencode-session` |
| Context window (K) | the window of the model (next table); ARTEX accepts at most `1000` |
| Output cap · max tokens | `32000` or more: these are reasoning models |

Live list of models and ids:

```bash
curl -s https://opencode.ai/zen/go/v1/models | jq -r '.data[] | "\(.id)\t\(.limit.context)"'
```

### 4.4 Several profiles at once

Profiles are independent rows in the database: create one per provider and switch the
active one per task (**Tasks → new task → LLM profile**), or point a task at several so
the pool fails over. `ARTEX_LLM_*` only seeds the default; anything you create in the UI
wins.

## 5. Where the data lives

Two bind mounts, both written as uid/gid `10001`:

| Path | Contents |
| --- | --- |
| `./data` | JWT key, the traffic SQLite index, recorded request/response bodies, evidence, transcripts, workspace files |
| `./skills` | The skills the agents can use; editable from the UI without a rebuild |

They are bind mounts so you can back them up and inspect them with the tools you already
have. Both must exist and be writable by the container's uid before the first start:

- **Run as yourself (recommended, no privileges):** `mkdir -p data skills` and set
  `ARTEX_UID=$(id -u)` / `ARTEX_GID=$(id -g)` in `.env` (or in front of the command). The
  container then writes as the same user that owns the directories.
- **Keep the image user (uid/gid 10001):** give it the directories once —
  `mkdir -p data skills && chown -R 10001:10001 data skills` — as root.

If the directories are missing, Docker creates them owned by root, the container cannot
write them, and the first start fails with a permission error. `./install.sh` does the
"run as yourself" path for you.

**Prefer nothing of the host to be writable?** Use the overlay; it swaps both for named
volumes, which need no `chown` and cannot touch the host filesystem:

```bash
docker compose -f docker-compose.yml -f docker-compose.volumes.yml up -d
docker compose cp artex:/app/data ./backup-data     # inspect/copy when you need it
```

## 6. Optional: read-only root filesystem

If you upgrade by pulling a new image instead of using the UI's one-click update, freeze
the container filesystem too:

```bash
docker compose -f docker-compose.yml -f docker-compose.readonly.yml up -d
```

The overlay sets `read_only: true` and keeps `/tmp` on tmpfs. Inside that mode the UI
update button cannot replace the binary (`/app` is immutable); upgrade with:

```bash
docker compose pull && docker compose up -d
```

Data is unaffected: it lives in the volumes/mounts above, not in the image layer.

## 7. Optional: cut the container off from your LAN

By default a bridge container can reach services published on the host and other machines
on your network. If your targets are only on the internet, block the private ranges on the
host (root required; the rules are lost on reboot unless you persist them):

```bash
# 1) find the two compose subnets (they are dynamic; read them, do not guess)
docker network inspect artex_app_net -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}'
docker network inspect artex_db_net  -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}'

# 2) allow artex -> postgres first, then reject private ranges from the app network
APP=172.18.0.0/16   # <-- value from the first command
DB=172.19.0.0/16    # <-- value from the second command
iptables -A DOCKER-USER -s "$APP" -d "$DB"            -j ACCEPT
iptables -A DOCKER-USER -s "$APP" -d 10.0.0.0/8       -j REJECT
iptables -A DOCKER-USER -s "$APP" -d 172.16.0.0/12    -j REJECT
iptables -A DOCKER-USER -s "$APP" -d 192.168.0.0/16   -j REJECT
iptables -A DOCKER-USER -s "$APP" -d 169.254.0.0/16   -j REJECT
```

Order matters: the `ACCEPT` for postgres must come before the `REJECT`s, and if a job
later needs a LAN target, add a specific `ACCEPT` for that address above them. Re-check
the subnets after `docker compose down && up`, because Docker may allocate different ones.

## 8. Operations

```bash
docker compose logs -f artex                  # follow the application log
docker compose logs artex | grep -F '[auth]'  # find the first-run setup token
docker compose restart artex                  # restart without touching the database
docker compose down                           # stop (data stays in ./data and pgdata)
docker compose pull && docker compose up -d   # upgrade to the newest published image
docker compose down -v                        # DESTROYS pgdata and the named volumes
```

Backups: stop nothing, copy `./data`, `./skills` and dump the database:

```bash
docker compose exec postgres pg_dump -U artex artex > artex-$(date +%F).sql
```

Health and troubleshooting:

```bash
docker compose ps                              # both services should report healthy
curl -fsS http://127.0.0.1:8787/api/health      # {"status":"ok"}
docker compose exec artex ls -l /app/data       # ownership must be 10001
docker compose exec artex df -h /tmp            # tmpfs usage (ARTEX_TMP_SIZE to raise)
```

If the container exits immediately, `docker compose logs artex` almost always says why:
the two common causes are a missing `POSTGRES_PASSWORD` and `./data` owned by another uid.

## 9. Known limitations (conscious decisions)

- **Root filesystem is writable by default.** The one-click update in the UI replaces
  `/app/artex`, which needs a writable layer. §6 has the read-only overlay for the
  image-only upgrade model.
- **Raw-socket scans depend on the runtime.** `NET_RAW` is granted and ICMP works through
  `net.ipv4.ping_group_range`, but a non-root user only keeps the capability across
  `execve` on runtimes that propagate it. The one-line probe in §1 tells you; if it fails,
  the agent falls back to connect-scans (`-sT`) and only SYN/OS-detection is lost.
- **The update checksum is unsigned.** The updater accepts a new binary only after
  comparing it against the `SHA256SUMS` file of the same GitHub release
  (`selfupdate/github.go`, `selfupdate/stage.go`). Over HTTPS against a GitHub-hosted file
  this catches corruption and CDN tampering; it does **not** protect against a compromised
  maintainer account or a hijacked release. A real fix is a detached signature over
  `SHA256SUMS` (minisign or Sigstore) with the public half compiled into the binary — that
  needs a release signing key, so it is documented rather than faked.
- **Chromium runs without its own sandbox** (Playwright's default). The security boundary
  is the container, not the browser.
- **`/tmp` is RAM.** Its cap (`ARTEX_TMP_SIZE`, default 2 GB) is not part of a memory
  limit; if you run very large downloads, raise it.
- **The image is not byte-reproducible.** Node is pinned (version + SHA256 of the release
  tarball) and the base images and `postgres:16-alpine` are pinned by digest, but the
  globally installed Playwright packages (`@playwright/mcp`, `playwright`,
  `@playwright/cli`) resolve at build time, so the Chromium build inside them can move.
- **Benchmark stack is separate and unhardened on purpose.**
  `docker-compose.bench.yml` uses `network_mode: host` because it has to reach the
  benchmark's target network; it builds a local-only `Dockerfile.bench` that is not part of
  this repository. Do not use it as a deployment template.

## 10. Verify the posture

`deploy/posture_test.go` parses `docker-compose.yml`, both overlays and the `Dockerfile`,
and asserts the invariants above: no `privileged`, no `network_mode: host`, no Docker
socket, `cap_drop: ALL` with `cap_add` limited to `NET_RAW`, `no-new-privileges`, non-root
`user`, loopback-only published ports, `pids_limit`, log rotation, healthcheck, a postgres
service on an internal network with no published port, a digest-pinned base image, a
declared `STOPSIGNAL`/`HEALTHCHECK`, and Node installed from a checksummed tarball. It also
fails if a `*-hardened` stack comes back. It is an ordinary Go test, so the existing CI gate
runs it; locally:

```bash
go test ./deploy/ -v
```
