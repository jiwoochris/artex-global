---
name: api-recon
description: Call this skill when collecting a website's API endpoints.
---

# API Recon (frontend interface reconnaissance)

With **authorization**, discover as completely as possible: the **backend API** (paths, methods,
parameters, response bodies), the **frontend routes**, and the **UI feature triggers** (tabs, dialogs,
table actions, and so on).

---

## Boundaries and prohibitions (agent must read · crossing them is out of scope)

This skill does **API and parameter-surface reconnaissance only**. It is not a vulnerability-hunting
or exploitation phase.

### Task boundaries

| Area | Allowed | Prohibited |
|---|---|---|
| **Target** | Enumerate paths, methods, parameters, routes, UI triggers | SQLi/XSS/authorization-bypass/brute-force/fuzzing for vulnerabilities, packet tampering attacks, destructive operations |
| **Authentication** | Hook + stub/mock to bypass the **client-side** login gate | Asking the user for account credentials or guessing them; attempting real login-form submission |
| **Runtime** | With no credentials, hook the interfaces and use mock responses to get the SPA past the login shell | Flows that can only continue with a real backend session |

### Credential-free dynamic analysis (Phase 3 default)

1. **Intercept and stub** the bootstrap interfaces (login, permissions, menus) through `preload.js` /
   `runtime_harvest.js`;
2. Return a mock body to business query interfaces that is **structurally correct, with a successful
   business code and empty or arbitrary data**;
3. Let the frontend render the post-login page even with no backend or a 401, so that more
   XHR/fetch/WebSocket traffic is triggered;
4. **Empty data, blank tables and placeholder UI are expected** — do not switch to a real login or to
   vulnerability testing because of them.

**In one sentence**: use mocks to open up the frontend routes and component mounts and **record only
outbound requests**; what the backend returns does not matter, what matters is **which further
endpoints the frontend sends**.

### Hard process prohibitions

| Prohibited | Instead |
|---|---|
| Before Phase 1 completes, grep/curl/Read the main entry `index-*.js` to extract API paths | Run `OUTDIR/harvest_static.py` |
| Hand-writing an `extract_apis.py` or similar script to replace harvest | Modify `OUTDIR/harvest_static.py` and re-run |
| Repeating the same grep/command after it failed ≥2 times | Change strategy: read tool_logs, change harvest, consult the reference |
| Skipping gate A/B and running the `scripts/` originals directly | Copy them into OUTDIR and adapt them to the target |
| Real usernames/passwords, OTP, OAuth and other authentication | stub/mock (see above) |
| Skipping the stub "to get real data" and doing authorization-bypass/injection tests | Record outbound only; that is the reconnaissance boundary |
| Irreversible operations such as deleting, exporting sensitive data, or bulk writes | The same applies to coverage clicking |
| Claiming all pages and endpoints were obtained without finishing runtime + dynamic enumeration | See "Definition of done", or state the limitation |
| Claiming all parameters are known without the parameter-trigger matrix + diff | Phase 3b matrix + Phase 5 diff |
| Inferring required/optional from a single runtime sample | Multi-sample diff, or validation rules / error-driven inference |

---

## Two-layer model + run modes

| Layer | Output | Limit |
|---|---|---|
| **Static** (JS bundle) | Complete endpoint paths, draft routes, candidate fields at the packing call site | No HTTP method; parameters need Phase 1b; runtime-concatenated URLs are missed |
| **Runtime** (live session) | Method + body + response + dynamic URL + WS/SSE; multi-sample diff completes the parameters | A page must actually render before it sends requests; one sample cannot settle required/optional |

| Run mode | Engine | Good for |
|---|---|---|
| **depth** | `runtime_harvest.js` (Puppeteer) | API inventory, METHOD/params/response body, WS/SSE, reproducible batch runs |
| **coverage** | browser + `preload.js` | Clicking tabs/dialogs/tables for deeper feature-point coverage |
| **both** | depth first, then coverage | Most complete, slowest |

**Parameter methodology** (there is no universal script): paths by harvest/regex; parameters by
**anchor window expansion + UI binding chain + multi-sample diff + error-driven inference** (the grep
recipes are in [reference.md](reference.md) section J).

---

## Definition of done

Reconnaissance may be called complete only when all of the following hold:

- [ ] **Static**: Phase 1 harvest produced `api_static.txt`, `routes.txt`, `js/`
- [ ] **Runtime**: at least one of depth or coverage; coverage/both need **the hook working + the
      dynamic enumeration loop**
- [ ] **Inside the shell**: business paths do not land on `/login` (watch for hash routing)
- [ ] **Parameters**: coverage/both completed the parameter-trigger matrix + `param_samples.json`;
      Phase 5 merged `params_merged.json`
- [ ] **Depth** (if module pages are blank): Phase 4 reconstructed the permission tree and re-ran
      until **module-level APIs** appear (not just locale/bootstrap)
- [ ] **Delivery**: Phase 5 outputs are complete (see the Phase 5 output table); `insert_assets`
      wrote the service and endpoint assets

---

## Scripts and gates

`scripts/` is a reference template only; running the originals directly and treating the result as
final is **prohibited**.

**Rule**: read first → adapt to the target → write into `OUTDIR` (e.g. `recon/`) → record it in
`CHANGES.md`; if it does not fit, rewrite it following the methodology, borrowing only the structure.

| Gate | When | Reference script → OUTDIR copy | Usually must change |
|---|---|---|---|
| **A (static)** | After Phase 0, before the **first** harvest/spider run | `harvest_static.py` / `spider_mpa.py` | **For most sites the default regex works as is**; change the endpoint regex, the webpack/Vite `publicPath`, or the MPA exclude/cookie only when the manifest or dialect does not match |
| **B (runtime)** | After Phase 2, before running depth/coverage | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage keys, the neutralize success value, stubs, the login regex, the api prefix, hash/history |

**Mandatory SPA order** (not interchangeable; the Phase numbering takes precedence over "explore
first, script later"):

| Step | Required | Prohibited |
|---|---|---|
| After Phase 0 completes | The next Bash command = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read of the main entry `index-*.js` (usually >500KB) |
| Gate A | Copy the script → make small target-specific changes → **run it immediately** | Extracting API paths by hand first and deciding afterwards whether to harvest |
| Before Phase 1 completes | Validate the output with `wc -l`; on 404 change harvest and retry | Hand-writing extract scripts; repeatedly grepping URLs that were not downloaded |
| From Phase 1b on | grep only `OUTDIR/js/*.js` | Using the main bundle instead of harvest |

- ✅ Copy `harvest_static.py` → (optionally) change the regex → **run it immediately**
- ❌ curl the main bundle → grep several times → write a temporary extract → harvest last
- **MPA**: the next Bash command after Phase 0 = `python3 OUTDIR/spider_mpa.py ...`

---

## Tools and output constraints

| Constraint | Explanation |
|---|---|
| Large files | Reading or grepping `index-*.js` files >100KB into the context is **prohibited**; batch-process them with the OUTDIR scripts |
| grep output | Always `\| head -20` or `-m 5`; keep only path summaries in the conversation, never paste bundle fragments |
| Validation | Use `wc -l`, `ls \| wc -l`; never Read a whole directory |
| Regex first look | Optional, ≤1 time, only on small chunks ≤50KB or on HTML; the official static result is harvest |
| reference | Recipes, templates and troubleshooting are in [reference.md](reference.md); do not inline the whole text again |

---

## Execution roadmap

```
Phase 0 classify + OUTDIR
  → Gate A → Phase 1 harvest (★ run immediately ★)
  → Phase 1b parameter reversing
  → Phase 2 the three auth gates → config.json
  → Gate B → Phase 3 runtime + parameter matrix
  → Phase 4 permission tree (if needed) → re-run Phase 3
  → Phase 5 merged report + insert_assets: bulk-insert every discovered service and endpoint/API asset; on no account may a discovered asset be omitted
```

Tick them in order; **the next Phase may not start until the previous item is complete**.

1. [ ] **Phase 0**: probe for SPA/MPA; create `OUTDIR` → [Phase 0](#phase-0--classification)
2. [ ] **Gate A + Phase 1**: copy the script → harvest **immediately** → validate with `wc -l` → [Phase 1](#phase-1--static)
3. [ ] **Phase 1b**: anchor window expansion + binding layer → `param_candidates.json` → [Phase 1b](#phase-1b--parameter-reversing)
4. [ ] **Phase 2**: the three auth gates → `config.json` → [Phase 2](#phase-2--the-three-auth-gates)
5. [ ] **Gate B**: adapt the runtime scripts → [Phase 3](#phase-3--runtime)
6. [ ] **Phase 3**: depth / coverage / both; confirm you are inside the shell; parameter-trigger matrix → `param_samples.json`
7. [ ] **Phase 4** (if needed): permission tree → patch stubs → re-run Phase 3 → [Phase 4](#phase-4--permission-tree-reconstruction)
8. [ ] **Phase 5**: merge the outputs + report + `insert_assets` → [Phase 5](#phase-5--merging-and-reporting)

---

## Phase 0 — classification

Fetch the entry HTML and **create `OUTDIR`** (do not modify `scripts/` inside the skill):

- **SPA**: empty shell + `<div id=app>` + chunks → Phases 1–5
- **MPA**: SSR + `<form>`, no endpoint bundle → after gate A:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

It produces `forms.txt`, `links.txt`, `api_inline.txt`. If an SPA yields forms ≈ 0 → move to Phase 1.

---

## Phase 1 — static

Follow [Scripts and gates](#scripts-and-gates) · [Tools and output constraints](#tools-and-output-constraints).

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: parse the HTML scripts → webpack/Vite manifest → download every lazy chunk → produce `js/`,
`api_static.txt`, `routes.txt`, `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- chunk count vs manifest: on a 404, change harvest and retry; do not curl chunks one by one by hand
- `api_static.txt` too small → relax the endpoint regex inside OUTDIR and re-run (see the reference)

### Phase 1b — parameter reversing

Paths come from Phase 1; parameter fields need their own reconnaissance. See
[Tools and output constraints](#tools-and-output-constraints) for the grep rules.

**Completion criteria**: for the important interfaces you can answer — field names, transport
location, inferred types, required or not, sample values, and confidence.

#### 1b.0 — Transport shape

| Shape | Where the parameters are | Look for statically |
|---|---|---|
| REST JSON | body + query | `(params\|data\|body)\s*:\s*\{` next to the path anchor |
| GraphQL | `variables` | gql templates, `$page: Int` |
| Classic form | urlencoded | `<form>`, `FormData` |
| File upload | multipart | `FormData.append` |
| Path parameters | `/user/:id` | route table + `useParams` / `$route.params` |
| Encrypted/signed | packed into `sign`/`data` | Hook the encryption function's inputs (reference section D) |

Output: annotate every interface with `transport: query|json|form|graphql|encrypted`.

#### 1b.1 — Anchor window expansion

Use a known path as the anchor and expand the window to find the packing object:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| Wrapper layer | Parameter clue |
|---|---|
| axios instance | `data` / `params` |
| Unified request helper | Interceptor injects global fields |
| OpenAPI client | Generated method signature |
| React Query / SWR | The hook's second argument |
| Vue composable | The composable's arguments |

Type remnants: `yup`/`zod`/rules, `Form.Item name=`, an embedded Swagger.

→ `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — Binding layer

```
Form field → onFinish/handleSubmit → transform → API payload
```

| Binding source | Technique |
|---|---|
| Form submit | Follow submit → transform → API |
| Table search | `getFieldsValue()` → `params` |
| Routing | `:id` / `?tab=` |
| Interceptors | Global `tenantId`, pagination, sign |
| Enum select | `options` → API enum values |

In the DevTools call stack, walk up from `fetch`/`XHR.send` to the packing function.

#### 1b.3 — The three packing questions (≠ the three auth gates of Phase 2)

| Question | What to answer |
|---|---|
| **Assembly** | Where the payload is built, traces of transforms |
| **Validation** | required, pattern, enum |
| **Transport** | path / query / body / multipart / headers |

The interceptor gate (Phase 2) incidentally reads the globally injected fields (Authorization,
`X-Tenant-Id`, sign).

#### 1b.4 — Hand-off to Phase 3

Candidate fields come from static analysis and the binding layer; **required/optional/conditional
dependencies** need the Phase 3 parameter matrix + diff and the Phase 5 error-driven inference.

---

## Phase 2 — the three auth gates

grep inside `OUTDIR/js/` (with `head`) and write the result into `config.json` (recipes in the
reference):

| Gate | Question | Keywords |
|---|---|---|
| **Render gate** | How does it decide you are logged in? | `isLogin`, `getToken`, Cookie/localStorage |
| **Interceptor gate** | What triggers a redirect to `/login`? | `response_code`, `errno`, axios interceptor |
| **Content gate** | Where do the menu and permissions come from? | `menu`, `permission`, `role`, `acl`, `routes` |

Never treat a localStorage key name as a credential — confirm it from the chunk or the request chain.

**Exit = gate B**: the conclusions go into `config.json`, and `OUTDIR/runtime_harvest.js` /
`preload.js` are modified accordingly.

### Phase 2b — API observation (optional)

Use `preload.js` inside OUTDIR to confirm the session key names, Authorization and nested API URLs:

| Setting | Output |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | Header observation |
| `extractUrlsFromResponse: true` | Sub-APIs inside responses |
| `observe.storageReads/cookieReads: true` | Feeds back into config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

Every coverage round exports: `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`,
`__API_RECON_OBSERVE__`.

---

## Phase 3 — runtime

Gate B must already have passed; follow
[Boundaries and prohibitions](#boundaries-and-prohibitions-agent-must-read--crossing-them-is-out-of-scope)
and the credential-free mock strategy.

Set `"runtimeMode": "depth" | "coverage" | "both"` in `config.json` (template in the reference).

### Hook and stub (shared by depth + coverage)

| Layer | Scope | Purpose |
|---|---|---|
| L1 exact | auth/permission/bootstrap stub | Get past the first-screen authentication |
| L2 negative correction | All JSON responses | Not-logged-in code → success |
| L3 fallback | `/api` and similar not matched by L1 | Empty success body, to open up the UI |

- **depth**: fake auth + `forward` rewriting the business code + `stubs`; walk `routes`
  (hash/history); produce `runtime_api.json`
- **coverage**: inject `preload.js` at **document-start** (CDP
  `addScriptToEvaluateOnNewDocument` or a Userscript)

Verify: `window.__API_RECON_PRELOAD__` exists; business paths do not bounce to `/login`.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage dynamic enumeration (mandatory)

1. Main navigation/sidebar — click every item, wait 1–3s for the network
2. Tabs — `role=tab`, `.ant-tabs-tab`
3. Tables — first-row view/edit/details
4. Toolbar — export, filter, create (**avoid irreversible deletion**)
5. Every module entered — merge APIs/routes
6. SPA — controlled `pushState` for paths not covered by `routes.txt` (prohibited on MPA)

**Parameter-trigger matrix** (mandatory): record each module once per operation type, then **diff the
samples**:

| Operation | Parameters usually added |
|---|---|
| List first page | Pagination + default filters |
| Clicking search | keyword, filter |
| Advanced filter | More optional fields |
| Create/edit | The full entity |
| Bulk/export/sort | `ids[]`, `exportType`, `sortField` |

**Under a stub the outbound body/headers are still real** — trust the request. Recordings →
`scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + document-start preload
- **React**: `routes.txt` + sidebar clicks + `pushState`
- **both**: 3a depth first, then 3b coverage

---

## Phase 4 — permission tree reconstruction

**Trigger**: module pages blank / every route gets only bootstrap calls (e.g. locale) → the content
gate did not pass.

| Symptom | Meaning |
|---|---|
| Inside the shell | The render gate and the interceptor gate passed |
| Sidebar entries missing / clicking yields blank | The stub shape or the permission codes are incomplete |
| Every route sends the same few APIs | The `v-if permission` check did not pass |
| `routes.txt` far smaller than the bundle | It must be completed from the auth module |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

Typical chain: `role_permissions` (flat codes) + `permissions/all` (tree) → `getResultTree` →
`userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Intermediate outputs: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`,
`perm_codes_all.txt`.

Stub checks: the outer `response_code` matches the interceptor gate; the flat codes line up with the
tree; `routes` covers every link in `route_map`.

After updating `config.json`, **re-run Phase 3**. For large SPAs, tune `waitUntil`, `routeTimeout` and
`perRouteMs` (see reference sections A3/I).

---

## Phase 5 — merging and reporting

### Output table

| File | Phase | Content |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | Static bundles and paths |
| `param_candidates.json` | 1b | Static parameter-field candidates |
| `config.json` | 2 | The three gates + runtime configuration |
| `runtime_api.json` | 3a | Detailed depth recording (including WS/SSE) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | Multi-sample, click log, detail |
| `route_map.json` etc. | 4 | Permission-tree intermediate files (if executed) |
| `params_merged.json` | 5 | Merged parameter fields + confidence |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | Routes, APIs, params, feature points, limitations |
| **insert_assets** | 5 | Write every service and endpoint asset into the asset store |

### 5b — parameter merging

Diff from `param_samples.json`; **there is no universal merge script**. For the confidence rules see
reference J7 (high/medium/low/pending-trigger).

### 5c — error-driven inference

Within the authorized scope you may send incomplete requests and read the 400 responses (**this is
parameter reconnaissance, not vulnerability testing**): `field 'x' is required`, enum errors, and so
on. Watch for the `data` wrapper, `variables`, and `bizData` before encryption.

The report must state: runtimeMode, the number of static/runtime APIs, parameter confidence,
uncovered modules, and a `CHANGES.md` summary relative to the reference scripts.

Recommended `site_map.json` structure:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

For more fields and grep recipes see [reference.md](reference.md).

---

## General notes

- **Framework agnostic**: webpack/Vite/Angular lazy loading works the same way
- **Transport**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web is out of scope
- **SSR**: client-side fetch can be recorded; RSC/Server Actions cannot be enumerated completely
- **Blind spots**: JSVMP, WASM, strong HMAC/mTLS validation → use static analysis and state the
  limitation
- **Parameter blind spots**: conditional coupling, hidden parameters, WASM packing → "pending trigger"
  / "unreachable"
- **Static is the safety net**: when runtime is blocked, static analysis can still enumerate endpoints

---

## Additional resources

- Grep recipes, the `config.json` template, troubleshooting, hooks, the parameter-reversing section J,
  and the site_map template: **[reference.md](reference.md)**
- For the reference script paths see the [Scripts and gates](#scripts-and-gates) table
