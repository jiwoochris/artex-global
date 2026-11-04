# ARTEX in Docker: hardened deployment and API keys

Guide for building ARTEX from source and running it in an unprivileged container,
isolated from the host but with internet egress, using **OpenCode Go** or
**OpenRouter** API keys.

> ⚠️ **Authorized use only.** ARTEX performs reconnaissance and exploitation
> autonomously. Use it only against systems you own or for which you have written
> authorization, and preferably against deliberately vulnerable labs (OWASP Juice
> Shop, DVWA). The full legal warnings are in the [README](../README.md).

---

## 1. Files this configuration adds

| File | What it is for |
| --- | --- |
| `Dockerfile.hardened` | Multi-stage image: builds the UI (Next.js) and the Go binary with `-tags embedui`, and runs the result as an unprivileged user. |
| `docker-compose.hardened.yml` | Full stack (artex + PostgreSQL) with its own bridge network, zero capabilities, read-only rootfs and resource limits. |
| this document | Operations, API keys and isolation verification. |

The original `Dockerfile` and `docker-compose.yml` stay the release-CI path (the
release workflow uses them to publish `ghcr.io/yukiteruamano/artex-global`). They are not
hardened as a whole, but they ship the same two posture fixes: the process runs as the
same unprivileged `artex` user (uid/gid 10001), and the recording proxy is bound to
container loopback. What they keep is a writable rootfs, so the one-click self-update
works there; see §8 for the exact difference.

---

## 2. Requirements

- Docker Engine 24+ with Compose v2 (`docker compose version`).
- ~4 vCPU, 8 GB of free RAM and ~4 GB of disk for the image (Chromium included).
- An API key: OpenRouter or OpenCode Go (or any OpenAI-compatible endpoint).

---

## 3. Startup

```bash
cp .env.example .env
```

Edit `.env` at least:

```dotenv
# Password of the internal PostgreSQL. Use only URL-safe characters
# (letters, digits, - _ .): it is inserted verbatim into the connection string.
POSTGRES_PASSWORD=change-this-to-something-long

# Initial password of the panel (8+ characters). If you leave it empty, the setup
# token appears in `docker compose logs artex` and must be entered by hand at /setup.
ARTEX_ADMIN_PASSWORD=

# Agent output language: en · es (English is the default)
ARTEX_LANG=en
```

Build and start:

```bash
docker compose -f docker-compose.hardened.yml up -d --build
docker compose -f docker-compose.hardened.yml ps
```

Open **http://127.0.0.1:8787**. The port is published on the host loopback only;
the interception proxy (8788) is never published.

On the first start, if you did not set `ARTEX_ADMIN_PASSWORD`:

```bash
docker compose -f docker-compose.hardened.yml logs artex | grep -F '[auth]'
# copy the token and enter it at http://127.0.0.1:8787/setup
```

---

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
docker compose -f docker-compose.hardened.yml up -d
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

Catalog of the Go plan at the time of writing (context in tokens):

| Model | Context | Model | Context |
| --- | --- | --- | --- |
| `glm-5.3` / `glm-5.3-flash` | 1M | `kimi-k3` / `kimi-k3.1-pro` | 1M |
| `deepseek-v4-pro` / `v4.1-flash` | 1M | `minimax-m3` | 1M |
| `qwen3.8-max` / `-flash` | 1M | `mimo-v2.6-pro` / `-flash` | 1M |
| `claude-haiku-5-5` | 1M | `grok-4.7` | 500K |
| `gpt-6-luna` / `gpt-5.6-luna` | 1.05M | `kimi-k2.7-code` | 262K |

Then: **Test connection** → **Set active**.

> If you are going to use reasoning models, raise `max_tokens`: with a small budget
> the model spends the turn thinking and the visible answer comes out empty.

#### Alternative without the UI (seeding the profile through the API)

To automate the deployment, the same profile can be created over REST. You must be
authenticated, so first obtain a token with the admin password (the same one you put
in `ARTEX_ADMIN_PASSWORD`):

```bash
ARTEX=http://127.0.0.1:8787
read -rs ARTEX_PASSWORD  # it does not end up in the shell history

TOKEN=$(curl -sS "$ARTEX/api/auth/login" -H 'Content-Type: application/json' \
  -d "{\"password\":\"$ARTEX_PASSWORD\"}" | jq -r .token)

ID=$(curl -sS "$ARTEX/api/llm/profiles" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{
    "name": "OpenCode Go",
    "format": "openai",
    "base_url": "https://opencode.ai/zen/go/v1",
    "model": "glm-5.3",
    "api_key": "'"$OPENCODE_API_KEY"'",
    "session_header_key": "x-opencode-session",
    "context_window_k": 1000,
    "max_tokens": 32000,
    "streaming": true
  }' | jq -r .id)

curl -sS "$ARTEX/api/llm/profiles/active" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"id\": $ID}"
```

If you prefer not to paste the key on the command line, create it from the UI: it
takes a minute and there is nothing to debug.

### 4.4 Several profiles at once

Each profile stores its own key. Enable rotation in **System → LLM → Rotation
settings** so that the agents move to the next profile when the current one runs out
of credit, expires or returns 429/5xx.

---

## 5. What the isolation does

`Dockerfile.hardened` + `docker-compose.hardened.yml`:

| Measure | Effect |
| --- | --- |
| `user: 10001:10001` (user `artex`, no shell) | Nothing runs as root, not even if an agent launches commands. |
| `cap_drop: [ALL]` | No `NET_RAW`, `SYS_ADMIN`, `NET_ADMIN`… even under an escalation there are no privileges to gain. |
| `security_opt: no-new-privileges` | setuid binaries of the system cannot elevate privileges (the setuid bit is also stripped in the image). |
| `read_only: true` + volumes | The rootfs is immutable: the agent only writes to `/app/data`, `/app/skills` and `/tmp`. |
| `tmpfs /tmp` (2 GB, `nosuid`,`nodev`) | Working space in RAM, without touching the host disk. |
| Bridge network `app_net` (172.30.0.0/24) | Internet egress allowed; there is no `network_mode: host`. |
| Internal `db_net` network (172.31.0.0/24) | PostgreSQL has no route to the internet or to the LAN. |
| No host mounts | No `/var/run/docker.sock`, no `~/.ssh`, no bind-mounted `./data`: named volumes. |
| `mem_limit`, `cpus`, `pids_limit`, `ulimits`, `shm_size` | An `nmap` loop or Chromium cannot take the host down through RAM, CPU or PIDs. |
| `logging` with rotation | The logs do not fill the host disk. |
| Only `127.0.0.1:8787` published | The UI/API port is not exposed to the local network. |

Quick check (the three commands must show what is indicated):

```bash
COMPOSE="docker compose -f docker-compose.hardened.yml"

# effective user inside the container → uid=10001 gid=10001
$COMPOSE exec artex id

# read-only rootfs, empty capabilities, no-new-privileges
docker inspect "$($COMPOSE ps -q artex)" \
  --format 'readonly={{.HostConfig.ReadonlyRootfs}} capdrop={{.HostConfig.CapDrop}} sec={{.HostConfig.SecurityOpt}} user={{.Config.User}}'

# internet egress OK (200); access to the LAN depends on the rules of step 5.1
$COMPOSE exec artex curl -fsS -o /dev/null -w '%{http_code}\n' https://openrouter.ai/api/v1/models
```

### 5.1 Optional: also cut off access to the LAN and the host

By default a bridge container can reach host services published on `0.0.0.0` and
machines on your local network. If your targets are only on the internet, block it on
the host (requires root, it is lost on reboot unless you persist it):

```bash
# 1) allow artex -> postgres (internal db_net network)
iptables -A DOCKER-USER -s 172.30.0.0/24 -d 172.31.0.0/24 -j ACCEPT
# 2) block private ranges: LAN, host gateway, link-local, other docker networks
iptables -A DOCKER-USER -s 172.30.0.0/24 -d 10.0.0.0/8     -j REJECT
iptables -A DOCKER-USER -s 172.30.0.0/24 -d 172.16.0.0/12  -j REJECT
iptables -A DOCKER-USER -s 172.30.0.0/24 -d 192.168.0.0/16 -j REJECT
iptables -A DOCKER-USER -s 172.30.0.0/24 -d 169.254.0.0/16 -j REJECT
```

The order matters: the postgres `ACCEPT` must come before the `REJECT`s. If you later
need to attack a target on your LAN, add a specific `ACCEPT` for that IP before the
`REJECT`s.

---

## 6. Known limitations (conscious decisions)

- **The update checksum is unsigned.** The one-click updater accepts a new binary only
  after comparing it against the `SHA256SUMS` file of the same GitHub release
  (`selfupdate/github.go`, `selfupdate/stage.go`). That comparison happens over HTTPS
  against a GitHub-hosted file, so it catches corruption and CDN tampering — it does
  **not** protect against a compromised maintainer account, a hijacked release, or a
  compromised GitHub account/token: whoever can replace the binary can replace the sum
  that vouches for it. In this hardened image the risk is moot (the binary is read-only
  and the update is disabled), but it applies to the published image and to the
  plain-binary install. A real fix is a detached signature: sign `SHA256SUMS` in the
  release job (minisign or Sigstore/cosign, key held by the maintainer), compile the
  public half into the binary, and verify the signature before parsing the sums. That
  requires a signing key under release control, so it is documented rather than faked.
- **Raw-socket scans are not available.** Without `CAP_NET_RAW`, `nmap -sS`, `-O`,
  `-sU` and the like fail; the agent must use `-sT` (connect scan) and the rest of
  the tools work the same (ICMP is enabled through `net.ipv4.ping_group_range`,
  without needing capabilities). If a specific job requires raw scanning, it is a
  conscious trade-off: `cap_add: [NET_RAW]` on the `artex` service.
- **The "one-click" update of the UI is disabled.** The rootfs is read-only, so the
  binary cannot replace itself. Update by rebuilding the image:
  ```bash
  git pull && docker compose -f docker-compose.hardened.yml up -d --build
  ```
  The data survives because it lives in volumes.
- **Chromium runs without its own sandbox** (Playwright's default behaviour). The
  security boundary here is the container, not the browser.
- **`/tmp` is RAM.** Its cap (2 GB) counts inside `mem_limit: 8g`; if you raise one,
  raise the other.
- **The image is not byte-reproducible.** Node is pinned (version + SHA256 for the
  release tarball, `ARG NODE_VERSION` in both Dockerfiles) and the base images and
  `postgres:16-alpine` are pinned by digest (multi-arch index), but the globally
  installed Playwright packages (`@playwright/mcp`, `playwright`, `@playwright/cli`) are
  still resolved at build time, so the Chromium build inside them can move. Pin them in
  both Dockerfiles if you need a fixed browser.
- **Data lives in named volumes** (`artex-secure_artex_data`,
  `artex-secure_artex_skills`, `artex-secure_pgdata`), not in `./data`: there is no
  bind mount so that the agent cannot create arbitrary files on the host.

---

## 7. Operations

```bash
COMPOSE="docker compose -f docker-compose.hardened.yml"

$COMPOSE logs -f artex          # backend logs (/setup token, errors)
$COMPOSE exec artex sh          # shell INSIDE the container (user 10001)
$COMPOSE restart artex
$COMPOSE down                   # stop (the volumes are kept)
$COMPOSE down -v                # stop and DELETE all data
```

Backup (artex and postgres are two separate things):

```bash
# ARTEX files: SQLite, jwt.key, traffic, evidence
docker run --rm -v artex-secure_artex_data:/d:ro -v "$PWD":/b alpine \
  tar czf /b/artex-data-$(date +%F).tgz -C /d .

# database (adjust the user if you changed POSTGRES_USER)
$COMPOSE exec -T postgres pg_dump -U "${POSTGRES_USER:-artex}" "${POSTGRES_DB:-artex}" | gzip > artex-db-$(date +%F).sql.gz
```

Adjust `mem_limit`, `cpus` and `shm_size` in the compose file if your hardware is
smaller; below ~4 GB and 2 vCPU the browser and the scans become unstable.

---

## 8. Alternative: use the published image

If you do not want to build from source, replace the `build:` block of `artex` in
`docker-compose.hardened.yml` with the project image and keep the rest of the
hardening as it is:

```yaml
    image: ghcr.io/yukiteruamano/artex-global:${ARTEX_TAG:-latest}
```

That image also runs as uid/gid 10001 (`artex`), so the `user:` line stays valid — but
its rootfs is writable and it carries no `read_only`/tmpfs, so two of the layers above
(`read_only`, and the assumption that the binary cannot rewrite itself) no longer hold;
the remaining ones (capabilities, networks, limits, named volumes) still do. For
maximum isolation, use the image built by `Dockerfile.hardened`.

### 8.1 The published `docker-compose.yml` (non-hardened) stack

That stack bind-mounts `./data` and `./skills` from the checkout instead of named
volumes, and the container's uid is 10001, so make those two directories writable by it
once, before the first start:

```bash
sudo chown -R 10001:10001 data skills
```

Otherwise the app cannot create `jwt.key`, the SQLite file or the traffic store and
fails to start. Its recording proxy (8788) is bound to container loopback by default
(no authentication — see §5), and publishing the port on the host does not
change that. Routing your own browser through it is an explicit opt-in:

```dotenv
# .env — only on a host you control; makes the unauthenticated MITM proxy reachable
# from the host (published on 127.0.0.1:8788 only, because of the ports mapping).
ARTEX_PROXY_ADDR=0.0.0.0:8788
```

Never set it to an address reachable from a network you do not control: the proxy
terminates TLS for anything that uses it and is not authenticated.

That stack sets `no-new-privileges` but deliberately **not** `cap_drop: [ALL]`: it ships
`nmap`/`ping` and the agent's raw-socket work (`nmap -sS`, `-O`, ICMP) needs those
capabilities, so dropping them would silently break the scans instead of hardening
anything. The hardened stack is where the capabilities go away (see §5 and §6).
