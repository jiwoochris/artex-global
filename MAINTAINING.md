# Upstream Sync and Preventing Translation Drift (Maintainer Guide)

This document lays out the procedure a **maintainer** follows to keep up with changes in the
upstream repository [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX) (currently unreachable, checked 2026-10) while maintaining the
the global edition. Contribution scope, legal responsibility, and the localization policy live in
[CONTRIBUTING.md](CONTRIBUTING.md); user-facing guidance lives in [README.md](README.md).
This document therefore focuses solely on **how that policy is actually enforced**.

The core goal of the localization can be summed up in one sentence: **preserve the original's
judgment performance exactly, while translating only the user-facing output into the selected language (English by default)**. That
boundary is easy to blur every time upstream updates, so the procedures and checks below prevent
translation drift.

---

## 1. The localization structure at a glance

This repository **forks** upstream ARTEX and stacks localization commits on top of its
history. Every commit on upstream `main` is contained in this repository's history, with the
localization commits added above them. Bringing in an upstream change therefore becomes a matter of
"inspecting the difference against upstream `main`, then separating what to preserve from what to
translate and applying each accordingly."

The deliverables fall into three groups.

- **Assets kept in the original language** (section 2). Translating them breaks performance or the
  ability to diff against upstream.
- **Output-language enforcement fixed in code.** `langDirective()` in `agent/prompt.go` appends to
  the end of each role's system prompt the instruction "write user-facing output in the selected language."
- **User-facing strings translated for each display language.** The UI strings live in `web/messages/{en,es}.json`
  (English is the source; Spanish is the only translation);
  the server's user-facing response strings live in named constants in each Go file.

---

## 2. Assets preserved in the original language (do not translate)

The following assets are kept as upstream ships them and are not translated per locale. When an
upstream change touches these assets, **apply it as is, without translating**.

- **The agent's internal reasoning prompts (the "brain" body).** These are the behavioral-instruction
  bodies in `agent/promptcatalog.go` and the DB seed `agent_prompts`. The behavior was benchmarked
  in the original Chinese edition, so per-locale translation introduces drift in the agent's judgment.
- **Strings that serve both as display and as agent input.** Some text that appears on the activity
  timeline while also being fed back as planner/reporter input context (task-abort reasons,
  interception-block messages, traffic-evidence helpers, and so on) is kept in the original, because
  a single record serves two purposes. The rationale is recorded case by case under the "brain
  boundary records" in `work/DECISIONS-FOR-JIWOO.md`.
- **The English documents and source strings.** Documentation, comments and skills are written in
  English only, one canonical file per subject. UI strings are English source (`en.json`) with the
  Spanish overlay (`es.json`). Upstream documents in other languages are not copied into this
  repository; they are compared against the upstream repository instead.
- **Command, payload, code, URL, identifier, and log originals.** These are the originals needed for
  analysis, so they are not translated.

---

## 3. The upstream tracking base

The upstream remote must be configured as follows. If it is missing, add it.

```bash
git remote add upstream https://github.com/Autumn-27/ARTEX
git remote -v   # check that upstream shows up
```

The upstream base commit that the localization has finished applying is:

- **Base = `d003372`** (upstream `main`, 2026-10-03, merge of PR #189 `fix/sse-same-origin`).

This value means "every upstream change up to this commit is already folded into this repository."
Each time you apply a new upstream change, update this base using the method in section 7.

---

## 4. The procedure for bringing in upstream changes

### 4.1 Fetch upstream and inspect the difference

```bash
git fetch upstream
git rev-list --count d003372..upstream/main        # number of unapplied upstream commits
git log --oneline d003372..upstream/main           # list of unapplied commits
```

`git fetch` only updates upstream's remote-tracking branch, so it leaves the working tree and `HEAD`
untouched. If the count of unapplied commits is 0, you are in sync with upstream and there is nothing
more to do.

### 4.2 Classify the changed files

See which files the unapplied commits touched, and split them into the preserved assets of section 2
and the translation targets.

```bash
git log --name-status --oneline d003372..upstream/main
```

The classification criteria are as follows.

- If `agent/promptcatalog.go` / the `agent_prompts` seed, or the display-and-input strings of
  section 2, changed → **apply as is, without translating**.
- If Go backend logic (`db/`, `llmrec/`, `server/`, and so on) changed → apply the logic as is, but
  check for **newly introduced user-facing strings** (`writeErr`, and the like) and write them as
  English source strings, adding the Spanish catalog entry.
- If the UI (`web/src/**`) changed and introduced **new screen strings** → do not hard-code them;
  add them under the same key to `web/messages/en.json` (source) and `web/messages/es.json`
  (translation).
- If **upstream indicators pinned by the detection rules** changed (the prober User-Agent in
  `enrich/enrich.go`, the self-update User-Agent in `selfupdate/`, the audit marker in
  `guard/guard.go`, the destructive-command deny list in `db/db.go`, the default listen and
  recording-proxy ports in `cmd/artex/main.go`) → bring the Sigma/Suricata rules and the ATT&CK layer
  in `detections/`, as well as the values in `detections/indicators/artex_indicators.csv`, in line
  with the new values. These indicators are not translation targets but the **basis of detection**,
  so when upstream changes a value, the rules silently go stale. The indicator-match test in 5.4
  catches that mismatch automatically.

### 4.3 Apply

Merge or cherry-pick feature by feature, then translate the new strings you separated out in 4.2 into
Spanish. During the merge it is easy for `en.json` / `es.json` keys to fall out of sync or for an
source string to leak into a user-facing slot, so always run the checks in section 5 right after
applying.

> **Example (unapplied commits as of 2026-10-05).** The `git fetch upstream` result shows upstream
> `main` ahead at `b55ceb1`, with 2 commits (`86729b6`, the model-fallback approval-token metering
> feature, plus the merge commit `b55ceb1`) unapplied relative to base `d003372`. These commits touch
> Go logic such as `db/llm_usage.go`, `llmrec/llmrec.go`, `server/intercept.go`, and `server/server.go`,
> and `web/src/app/(main)/system/intercept/page.tsx`, `web/src/lib/api.ts`, `web/src/lib/mock/handler.ts`,
> and `web/src/lib/types.ts`. The maintainer therefore applies the Go logic as is and only extracts and
> translates the new screen strings introduced on the intercept settings page into `en.json` / `es.json`
> keys. (These two commits had not yet been applied at the time this document was written, so the base
> stays at `d003372`.)

---

## 5. Translation-symmetry and drift checks

After applying an upstream change or doing translation work, verify the following three things.

### 5.1 English ↔ Spanish message symmetry

The keys of `en.json` and `es.json` must be exactly the same: no key may exist in only one of them.
The script below prints the leaf-key counts and the symmetric difference (which must be 0).

```bash
python3 - <<'PY'
import json
en = json.load(open('web/messages/en.json'))
es = json.load(open('web/messages/es.json'))
def flatten(d, p=''):
    out = {}
    if isinstance(d, dict):
        for k, v in d.items(): out.update(flatten(v, p + '/' + k))
    elif isinstance(d, list):
        for i, v in enumerate(d): out.update(flatten(v, p + '/' + str(i)))
    else: out[p] = d
    return out
fe, fs = flatten(en), flatten(es)
print('en leaf keys :', len(fe))
print('es leaf keys :', len(fs))
print('key symdiff  :', len(set(fe) ^ set(fs)))        # must be 0
PY
```

The key count can grow as upstream changes are applied, but en and es must always be equal and
`key symdiff` must always be 0.

### 5.2 Confirm that the reasoning prompts are in English

The agent's reasoning-prompt body is in English. A non-zero count of lines containing Han
characters in the check below is a signal that an upstream merge pulled the Chinese body back in.

```bash
python3 -c "import re; han=re.compile(r'[㐀-鿿]'); t=open('agent/promptcatalog.go').read(); print('promptcatalog.go CJK lines =', sum(1 for l in t.splitlines() if han.search(l)))"
```

`promptcatalog.go CJK lines = 0` after the English conversion. If this number rises, check whether
the brain body was merged in its original language.

### 5.3 Make sure no untranslated language leaks into the build output

After exporting the UI statically, Chinese (or any language other than the active locale's) appearing
in the prerendered HTML means a missing translation or a leaked source string.

```bash
cd web && npm ci && NEXT_EXPORT=1 npm run build   # generates out/
# check that the visible text in out/**/*.html contains 0 Chinese characters
```

### 5.4 Make sure the detection indicators still match the upstream source

The rules in `detections/` are based on the strings that upstream actually emits (prober User-Agent,
self-update User-Agent, audit marker, destructive-command deny list). When an upstream re-sync changes
these values, the translation checks all pass while only the shipped rules silently stop matching. The
test below verifies bidirectionally that each indicator is still present in both the upstream source
and the rules, so run it after a re-sync.

```bash
detections/tests/indicators/run.sh   # runs in isolation under Docker; RESULT: PASS means a match
```

On failure it prints which indicator is out of sync and in which direction (whether the upstream
source changed or the rule changed), so bring the rules and the layer in line with the new values per
the last classification criterion of 4.2. This test also runs automatically in the repository CI
([`.github/workflows/detections.yml`](.github/workflows/detections.yml)) on every push/PR that changes
the rule tree or the upstream source files above, catching re-sync drift at the merge gate.

If you add a new indicator and in doing so **pin a new upstream source file** (as when adding the port
indicator from `cmd/artex/main.go`, for example), you must also add that file to the `push` and
`pull_request` `paths` filters of the workflow above. If you miss it, a PR that changes only that
source will not trigger the indicator test, and the drift will silently pass the merge gate. The
indicator test checks this synchronization itself (its fifth check, "CI triggers this test when any
pinned source changes"): if any non-`detections/` source the test reads is not listed in both `paths`
blocks, the test fails, so source pinning and CI trigger conditions cannot be merged out of sync.

### 5.5 When bumping the detection-test tool pins

The detection tests run `sigma-cli`, the SigmaHQ validator plugin (`pySigma-validators-sigmahq`), and
the Suricata image at fixed versions (the defaults in each `run.sh`, overridable via environment
variables). Bumping these pins can introduce **tool-side drift** rather than upstream-source drift. In
particular, the SigmaHQ validator adds new convention checks with each release, so
`detections/tests/sigma_lint/run.sh` may surface new issues in red. When that happens, bring the rules
in line with the new convention, or — if the convention does not fit a standalone rule set — record
the reason and add it to the exclusion list in
[`detections/tests/sigma_lint/validators.yml`](detections/tests/sigma_lint/validators.yml). If a
backend plugin changes its support, the `sigma_backends` test gives the same signal.

---

## 6. Finishing with build and test verification

After applying and translating, verify the backend and frontend per the
[development environment procedure in CONTRIBUTING.md](CONTRIBUTING.md#development-environment).
If you do not have Go locally, you can run the same thing under Docker.

```bash
docker run --rm -v "$PWD":/src -w /src \
  -v artexko-gomod:/go/pkg/mod -v artexko-gocache:/root/.cache/go-build \
  golang:1.26 sh -c 'go build ./... && go vet ./... && go test ./... -count=1'
```

When you translate a user-facing string, add a regression test (`*_localized_test.go`) that asserts
that string as well, so that if a later upstream change pulls another language back in, the test
catches it.
Always do translation verification with a capable (frontier-class) model. Low-cost, small models can
revert their output to the original language, so you must not judge whether a translation applied from
their output alone.

---

## 7. Base-update record

Once you have applied an upstream change and finished verifying it, **update the base commit value in
section 3 of this document to the new upstream commit** and include that change in the same commit or
a following one. Doing so lets the next maintainer confirm "how far things have been applied" in this
one place.

Commit messages follow the [commit-message rules in CONTRIBUTING.md](CONTRIBUTING.md#commit-messages).
For example, an upstream-sync commit is written like this (the description is in English).

```
chore(upstream): sync d003372..b55ceb1 (intercept token metering) + translate new UI strings
```

---

## 8. Review and verification rules of thumb (common pitfalls)

Here are two pitfalls maintainers repeatedly fall into when checking upstream applies, translations,
and documentation improvements. Both are cases where "the checking method itself is wrong, so you
mistake something healthy for broken," so they are fixed here as rules of thumb to prevent unnecessary
reverts.

### 8.1 Check the repository's CI status by specifying the repository

This repository is a fork of upstream ARTEX, so the local `git remote` has both `origin`
(jiwoochris/artex-global) and `upstream` (Autumn-27/ARTEX) registered (see section 3). In this state, if
you do not specify a repository in a `gh` command, `gh` **picks the upstream repository as the
default** and shows you run results from upstream, which does not have our workflows. You can then see
upstream CI green and **mistakenly think our CI passed**, or judge our workflows (`ci.yml`,
`detections.yml`) as "HTTP 404 ... not found" by mistake.

So when checking CI, always name the repository explicitly.

```bash
gh run list -R jiwoochris/artex-global --workflow ci.yml --limit 5
gh run list -R jiwoochris/artex-global --workflow detections.yml --limit 5
```

Once set, you can make `gh` default to our repository even when you omit `-R`. Note, however, that
this setting is a **local gh setting** and is not committed to the repository, so you must set it
again on a new machine or a new checkout.

```bash
gh repo set-default jiwoochris/artex-global
gh repo set-default --view   # check that jiwoochris/artex-global shows up
```

### 8.2 Check external links in documents with GET, like a browser

Section 7 of the defense guide ([`docs/defense.md`](docs/defense.md)) carries links to Korean official channels (boho.or.kr,
fsec.or.kr, pipc.go.kr). When checking whether these links are alive, using only `curl -I` (a HEAD
request) or the default User-Agent will **mistake a healthy link for a broken one**. Korean public and
security agency sites refuse a simple check for three reasons.

- **They reject HEAD requests.** For example, fsec.or.kr returns 400 to `curl -I` (HEAD).
- **They block the default `curl` User-Agent.** fsec.or.kr and pipc.go.kr return 400 even to GET
  requests sent with the default UA (they return 200 when sent with a browser UA).
- **They redirect to a different address.** pipc.go.kr redirects twice, from `www.pipc.go.kr` to
  `pipc.go.kr/np/`, so if you do not follow redirects you miss the final status.

So check links **with a browser User-Agent, with GET, following redirects**.

```bash
UA='Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36'
for u in https://www.boho.or.kr https://www.fsec.or.kr https://www.pipc.go.kr; do
  curl -sS -L -A "$UA" -o /dev/null -w "$u -> %{http_code} %{url_effective}\n" "$u"
done
```

A final status code of 200 means the link is valid. If the status code comes back 400 or 403, first
suspect that the link is not broken but that **your checking method was blocked by the server's access
policy**, and re-check by eliminating factors one at a time: HEAD, the default UA, and not following
redirects. (As of the 2026-10-06 check, all three links return 200 with the method above, with
pipc.go.kr returning 200 after two redirects.)

This manual procedure is automated as-is by `scripts/check-external-links.py`. It gathers the external
links outside code fences and inline code from every tracked `.md` (excluding reserved and placeholder
hosts), checks their status with a browser UA, GET, and redirect-following as above, and retries on
network errors, 5xx, and 429 to separate transient flakes from real outages. It sorts results into four
classes: OK (2xx/3xx) · RESTRICTED (401/403/405/429 — the host is alive, only the checking method is
blocked) · ALLOWED (a known upstream-inherited dead link we cannot fix, listed in
`scripts/external-links-allowlist.txt`) · DOWN (404/410/5xx/connection error — likely broken).

- Preview the targets without the network: `python3 -I scripts/check-external-links.py --list`
- Release/periodic check (exits non-zero on a newly broken link): `python3 -I scripts/check-external-links.py --strict`

External-link liveness is flaky, so it is **not a merge gate**. Instead the non-blocking
[`external-links`](.github/workflows/external-links.yml) workflow runs `--strict` every Monday and on
manual dispatch, turning red when a DOWN not on the allowlist newly appears. Dead links inherited by
upstream-preserved files (for example a vanished contributor account credited upstream) are
ones we cannot fix, so they go on the allowlist with a reason and are excluded from the strict check.

---

## 9. The release pipeline

Pushing a version tag (`v*`) makes [`.github/workflows/release.yml`](.github/workflows/release.yml)
build binaries for five platforms and, when a condition is met, a multi-architecture Docker image.
This fork has never cut a release tag, so this workflow has never run. This section therefore records
what the pipeline assumes and produces, and whether those assumptions match the current repository
structure. Because pushing a tag creates a GitHub Release on the public repository, cut a release only
after the publishing decision is made.

### 9.1 How to cut a release

Pushing a tag that starts with `v` fires the workflow.

```bash
git tag v0.3.15
git push origin v0.3.15
```

### 9.2 What the pipeline does

The workflow is split into five jobs.

- **frontend.** Statically exports the frontend once (`web/out`) and uploads that output as the
  `web-dist` artifact. The binaries job below downloads and reuses this output per target.
- **binaries.** Cross-compiles five targets (linux amd64/arm64, darwin amd64/arm64, windows amd64) and
  packages a zip per target. On the linux amd64 binary it runs an `artex -h` smoke test to confirm the
  binary actually runs.
- **release.** Gathers all zips, generates a `SHA256SUMS` checksum file, and creates a GitHub Release
  with the zips and the checksum attached.
- **docker.** Takes the linux binaries cross-compiled by binaries, builds a multi-architecture
  (amd64/arm64) image, and pushes it to GitHub Container Registry as `ghcr.io/jiwoochris/artex-global`
  with the release tag and `latest`. It logs in with the workflow's built-in `GITHUB_TOKEN` (job
  permission `packages: write`), so no extra secrets are needed.

### 9.3 Do the build assumptions match the repository structure

The pipeline runs on the following three assumptions, and all three were confirmed to match the
current repository structure by reproducing the binaries job locally.

- **Frontend embed.** The binaries job receives the `web-dist` (the contents of `web/out`) uploaded by
  the frontend job into `server/webui/dist`, and `//go:embed all:webui/dist` in `server/webui_embed.go`
  embeds that location into the binary. The binaries job therefore does not rebuild the frontend; it
  calls [`build.sh`](build.sh) with `ARTEX_SKIP_FRONTEND=1`.
- **Binary and package paths.** `build.sh --target <os>/<arch>` produces the `dist/artex-<os>-<arch>/artex`
  binary and a zip package under `dist/`. The zip contains the binary together with a start script
  (`start.sh` on Linux/macOS, `start.bat` on Windows), `skills/`, `config.example.json`, and
  `README.md`.
- **Copying the binary into the Docker image.** The binaries job uploads the linux binary separately as
  the `bin-linux-<arch>` artifact, and the docker job receives it as `dist/<arch>/artex`. The
  `COPY dist/${TARGETARCH}/artex` in [`Dockerfile`](Dockerfile) picks up that path via the `TARGETARCH`
  that buildx fills in per platform during a multi-architecture build. [`.dockerignore`](.dockerignore)
  does not exclude `dist/`, so the binary is included in the build context.

### 9.4 The Docker image namespace

Images are published to `ghcr.io/jiwoochris/artex-global`, a namespace this fork owns, and
`docker-compose.yml` pulls that image (decided 2026-10-11, `work/DECISIONS-FOR-JIWOO.md`, item 8).
Upstream's `autumn27/artex` has been taken down from Docker Hub and this fork has no push access to
it, so it is no longer used. A GHCR package may be created as private on its first publish, so after
the first release check in the package settings that it is public; if it is private,
`docker compose pull` fails with an authentication error.

### 9.5 Verifying locally without a tag

To check only the pipeline assumptions without cutting a public release, reproduce the binaries job
locally. If you do not have Go locally, you can run the same thing under Docker.

```bash
# 1) Static frontend export (corresponds to the frontend job in release.yml)
cd web && npm ci && npm run build:static && cd ..
# 2) Place it where the binaries job receives the artifact
rm -rf server/webui/dist && mkdir -p server/webui/dist && cp -a web/out/. server/webui/dist/
# 3) Build one target with the same environment as the binaries job
docker run --rm -v "$PWD":/app -w /app \
  -e ARTEX_SKIP_FRONTEND=1 -e ARTEX_SKIP_NPM_CI=1 \
  -e ARTEX_COMPRESS=0 -e ARTEX_PACKAGE=1 -e ARTEX_PACKAGE_DIR=dist \
  -e ARTEX_BUILD_VERSION=v0.0.0-local \
  golang:1.26 bash -c 'apt-get update && apt-get install -y zip && ./build.sh --target linux/amd64'
# 4) Confirm the outputs: dist/artex-linux-amd64/artex · dist/*.zip · dist/SHA256SUMS
```

`dist/artex-linux-amd64/artex` is a statically linked ELF, and given `-h` it prints usage and exits
with code 0. This is the behavior the binaries job's smoke test confirms. Build outputs
(`dist/`, `server/webui/dist/`) are not committed to the repository (they are excluded by
`.gitignore`).
