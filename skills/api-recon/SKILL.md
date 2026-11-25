---
name: api-recon
description: Invoke this skill when collecting a website's API endpoints.
---

# API Recon (Frontend API Reconnaissance)

Under **authorized** conditions only, discover as completely as possible: **backend APIs** (paths, methods, parameters, response bodies), **frontend routes**, and **UI feature trigger points** (tabs, modals, table actions, etc.).

---

## Boundaries and Prohibitions (Agent Must Read · Any Violation Is Overstepping)

This skill does **API / parameter-surface reconnaissance only**. It is not a vulnerability-hunting or exploitation phase.

### Task Boundaries

| Area | Allowed | Prohibited |
|---|---|---|
| **Goal** | Enumerate paths, methods, parameters, routes, UI trigger points | SQLi/XSS/privilege escalation/brute force/vulnerability fuzzing, request-tampering attacks, destructive operations |
| **Auth** | Hook + stub/mock to bypass the **client-side** login gate | Asking the user for, or guessing, usernames and passwords; attempting real login form submissions |
| **Runtime** | Hook APIs without credentials; use mock responses to bring the SPA into its post-login shell | Flows that can only continue with a real backend session |

### Credential-Free Dynamic Analysis (Phase 3 Default)

1. Use `preload.js` / `runtime_harvest.js` to **intercept and stub** login, permission, menu, and other bootstrap APIs;
2. For business query APIs, return a mock body that is **structurally correct, carries a success business code, and may contain empty data**;
3. Make the frontend render post-login pages even with no backend or in a 401 environment, so that it fires more XHR/fetch/WebSocket requests;
4. **Empty data, blank tables, and placeholder UI are all expected**. Do not switch to real login or vulnerability testing because of them.

**In one sentence**: use mocks to force open frontend routes and component mounting, and **record outbound requests only**. What the backend returns does not matter; what matters is **which other APIs** the frontend sends.

### Hard Process Prohibitions

| Prohibited | Do this instead |
|---|---|
| grep/curl/Read the main entry `index-*.js` to extract API paths before Phase 1 is complete | Run `OUTDIR/harvest_static.py` |
| Hand-writing `extract_apis.py` or other scripts that replace harvest | Modify `OUTDIR/harvest_static.py` and rerun it |
| Repeating the same grep/command after it has failed ≥2 times | Change strategy: read tool_logs, modify harvest, check reference |
| Skipping Gates A/B and running the original `scripts/` directly | Copy into OUTDIR and adapt to the target |
| Real usernames/passwords, OTP, OAuth, or other authentication | stub/mock (see above) |
| Skipping stubs on the pretext of "getting real data" to do privilege-escalation/injection testing | Record outbound only; that is the recon boundary |
| Irreversible operations such as deleting, exporting sensitive data, or bulk writes | The same applies to coverage clicks |
| Claiming all pages and APIs were obtained without completing runtime + dynamic enumeration | See "Definition of Done" or state the limitations |
| Claiming all parameters are known without completing the parameter trigger matrix + diff | Phase 3b matrix + Phase 5 diff |
| Inferring required/optional from a single runtime sample | Multi-sample diff, or infer from validation rules/errors |

---

## Two-Layer Model + Run Modes

| Layer | Output | Limits |
|---|---|---|
| **Static** (JS bundle) | Full set of endpoint paths, draft routes, candidate fields at payload-assembly sites | No HTTP methods; parameters need Phase 1b; misses URLs concatenated at runtime |
| **Runtime** (live session) | Method + body + response + dynamic URLs + WS/SSE; multi-sample diff fills in parameters | A page must actually render before it sends requests; a single sample is not enough to determine required/optional |

| Run mode | Engine | Use for |
|---|---|---|
| **depth** | `runtime_harvest.js` (Puppeteer) | API inventory, METHOD/params/response bodies, WS/SSE, reproducible batch runs |
| **coverage** | browser + `preload.js` | Clicking tabs/modals/tables for deeper feature coverage |
| **both** | depth first, then coverage | Most complete, takes the longest |

**Parameter methodology** (no generic script): paths come from harvest/regex; parameters come from **anchor window expansion + UI binding chain + multi-sample diff + inference from errors** (grep recipes in section J of [reference.md](reference.md)).

---

## Definition of Done

Recon may be declared complete only when all of the following are met:

- [ ] **Static**: Phase 1 harvest produced `api_static.txt`, `routes.txt`, `js/`
- [ ] **Runtime**: at least one of depth or coverage; coverage/both require **Hook in effect + dynamic enumeration loop**
- [ ] **In the shell**: visiting a business path does not land on `/login` (watch out for hash routing)
- [ ] **Parameters**: coverage/both completed the parameter trigger matrix + `param_samples.json`; Phase 5 merged `params_merged.json`
- [ ] **Depth** (if module pages are blank): Phase 4 permission tree reconstructed and rerun until **module-level APIs** appear (not just locale/bootstrap)
- [ ] **Delivery**: Phase 5 outputs complete (see the Phase 5 output table); `insert_assets` has written the service and endpoint assets

---

## Scripts and Gates

`scripts/` contains reference templates only. Running the originals directly and treating that as the final result is **prohibited**.

**Rule**: read first → adapt to the target → write into `OUTDIR` (e.g. `recon/`) → record in `CHANGES.md`; if a template does not fit, rewrite it following the methodology and borrow only its structure.

| Gate | When | Reference script → OUTDIR copy | Common required changes |
|---|---|---|---|
| **A (static)** | After Phase 0, before the **first** harvest/spider run | `harvest_static.py` / `spider_mpa.py` | **On most sites the default regex runs as-is**; only when the manifest/dialect does not match, change the endpoint regex, webpack/Vite `publicPath`, MPA exclude/cookie |
| **B (runtime)** | After Phase 2, before running depth/coverage | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage keys, neutralize success values, stubs, login regex, API prefix, hash/history |

**Mandatory SPA order** (not interchangeable; Phase numbering takes priority over "explore first, script later"):

| Step | Required | Prohibited |
|---|---|---|
| After Phase 0 | Next Bash command = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read the main entry `index-*.js` (usually >500KB) |
| Gate A | Copy script → minor changes as needed → **run immediately** | Manually extracting APIs first and then deciding whether to harvest |
| Before Phase 1 is complete | Check outputs with `wc -l`; on 404, modify harvest and retry | Hand-written extract scripts; repeatedly grepping URLs that were not downloaded |
| From Phase 1b onward | grep only `OUTDIR/js/*.js` | Using the main bundle instead of harvest |

- ✅ Copy `harvest_static.py` → (optionally) change the regex → **run immediately**
- ❌ curl the main bundle → grep several times → write a temporary extract script → harvest only at the end
- **MPA**: after Phase 0, next Bash command = `python3 OUTDIR/spider_mpa.py ...`

---

## Tool and Output Constraints

| Constraint | Details |
|---|---|
| Large files | Read/grep of `index-*.js` files >100KB into context is **prohibited**; batch-process with OUTDIR scripts |
| grep output | Must use `\| head -20` or `-m 5`; keep only path summaries in the conversation, do not paste bundle snippets |
| Verification | Use `wc -l`, `ls \| wc -l`; do not Read whole directories |
| Initial regex probe | Optional, ≤1 time, only on small chunks ≤50KB or HTML; harvest is authoritative for the formal static pass |
| reference | Recipes/templates/troubleshooting are in [reference.md](reference.md); do not duplicate the full text inline |

---

## Execution Roadmap

```
Phase 0 classification + OUTDIR
  → Gate A → Phase 1 harvest (★ run immediately ★)
  → Phase 1b parameter reverse engineering
  → Phase 2 three auth gates → config.json
  → Gate B → Phase 3 runtime + parameter matrix
  → Phase 4 permission tree (if needed) → rerun Phase 3
  → Phase 5 merged report + insert_assets bulk-inserts all discovered service and endpoint API assets; under no circumstances may an already discovered asset be left out of the insert
```

Check off in order; **do not enter the next Phase until the previous item is complete**.

1. [ ] **Phase 0**: initial SPA/MPA probe; create `OUTDIR` → [Phase 0](#phase-0--classification)
2. [ ] **Gate A + Phase 1**: copy scripts → harvest **immediately** → verify with `wc -l` → [Phase 1](#phase-1--static)
3. [ ] **Phase 1b**: anchor window expansion + binding layer → `param_candidates.json` → [Phase 1b](#phase-1b--parameter-reverse-engineering)
4. [ ] **Phase 2**: three auth gates → `config.json` → [Phase 2](#phase-2--three-auth-gates)
5. [ ] **Gate B**: adapt the runtime scripts → [Phase 3](#phase-3--runtime)
6. [ ] **Phase 3**: depth / coverage / both; confirm you are in the shell; parameter trigger matrix → `param_samples.json`
7. [ ] **Phase 4** (if needed): permission tree → patch stubs → rerun Phase 3 → [Phase 4](#phase-4--permission-tree-reconstruction)
8. [ ] **Phase 5**: merge outputs + report + `insert_assets` → [Phase 5](#phase-5--merge-and-report)

---

## Phase 0 — Classification

Fetch the entry HTML and **create `OUTDIR`** (do not modify `scripts/` inside the skill):

- **SPA**: empty shell + `<div id=app>` + chunks → Phases 1–5
- **MPA**: SSR + `<form>`, no endpoint bundle → after Gate A:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

Produces `forms.txt`, `links.txt`, `api_inline.txt`. If an SPA yields forms ≈ 0 → switch to Phase 1.

---

## Phase 1 — Static

Follow [Scripts and Gates](#scripts-and-gates) · [Tool and Output Constraints](#tool-and-output-constraints).

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: parse HTML scripts → webpack/Vite manifest → download all lazy chunks → produce `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- Chunk count vs. manifest: on 404, modify harvest and retry; do not manually curl chunks one by one
- Too few entries in `api_static.txt` → loosen the endpoint regex in OUTDIR and rerun (see reference)

### Phase 1b — Parameter Reverse Engineering

Paths come from Phase 1; parameter fields need separate recon. For grep rules see [Tool and Output Constraints](#tool-and-output-constraints).

**Completion criterion**: for important APIs you can answer: field name, transport location, inferred type, whether required, sample value, confidence.

#### 1b.0 — Transport Shape

| Shape | Where the parameters are | Look at first (static) |
|---|---|---|
| REST JSON | body + query | `(params\|data\|body)\s*:\s*\{` next to the path anchor |
| GraphQL | `variables` | gql templates, `$page: Int` |
| Traditional form | urlencoded | `<form>`, `FormData` |
| File upload | multipart | `FormData.append` |
| Path parameters | `/user/:id` | Route table + `useParams` / `$route.params` |
| Encrypted/signed | Wrapped in `sign`/`data` | Hook the encryption function's arguments (reference section D) |

Output: annotate each API with `transport: query|json|form|graphql|encrypted`.

#### 1b.1 — Anchor Window Expansion

Use a known path as the anchor and widen the window to find the payload-assembly object:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| Wrapper layer | Parameter clues |
|---|---|
| axios instance | `data` / `params` |
| Unified request helper | Interceptors inject global fields |
| OpenAPI client | Generated method signatures |
| React Query / SWR | Second argument of the hook |
| Vue composable | Composable arguments |

Type remnants: `yup`/`zod`/rules, `Form.Item name=`, embedded Swagger.

→ `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — Binding Layer

```
Form field → onFinish/handleSubmit → transform → API payload
```

| Binding source | Technique |
|---|---|
| Form submit | Follow submit → transform → API |
| Table search | `getFieldsValue()` → `params` |
| Route | `:id` / `?tab=` |
| Interceptor | Global `tenantId`, pagination, sign |
| Enum select | `options` → API enum values |

In the DevTools call stack, trace upward from `fetch`/`XHR.send` to the payload-assembly function.

#### 1b.3 — Three Payload-Assembly Questions (≠ the three auth gates of Phase 2)

| Question | What to answer |
|---|---|
| **Assembly** | Where the payload is built, traces of transforms |
| **Validation** | required, pattern, enum |
| **Transport** | path / query / body / multipart / headers |

The interceptor gate (Phase 2) also reads globally injected fields along the way (Authorization, `X-Tenant-Id`, sign).

#### 1b.4 — Handoff to Phase 3

Candidate fields come from the static/binding layers; **required/optional/conditional dependencies** need the Phase 3 parameter matrix + diff + Phase 5 inference from errors.

---

## Phase 2 — Three Auth Gates

grep in `OUTDIR/js/` (with `head`) and write the results into `config.json` (recipes in reference):

| Gate | Question | Keywords |
|---|---|---|
| **Render gate** | How does the app decide the user is logged in? | `isLogin`, `getToken`, Cookie/localStorage |
| **Interceptor gate** | What triggers a redirect to `/login`? | `response_code`, `errno`, axios interceptor |
| **Content gate** | Where do menus/permissions come from? | `menu`, `permission`, `role`, `acl`, `routes` |

Do not treat localStorage key names as credentials; they must be confirmed from chunks/the request chain.

**Exit = Gate B**: put the conclusions into `config.json`, and modify `OUTDIR/runtime_harvest.js` / `preload.js`.

### Phase 2b — API Observation (Optional)

Use the `preload.js` in OUTDIR to confirm session key names, Authorization, and nested API URLs:

| Setting | Output |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | Header observation |
| `extractUrlsFromResponse: true` | Sub-APIs inside responses |
| `observe.storageReads/cookieReads: true` | Back-fill config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

Export after every coverage round: `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`.

---

## Phase 3 — Runtime

Gate B must already be passed; follow [Boundaries and Prohibitions](#boundaries-and-prohibitions-agent-must-read--any-violation-is-overstepping) · the credential-free mock strategy.

Set `"runtimeMode": "depth" | "coverage" | "both"` in `config.json` (template in reference).

### Hooks and Stubs (shared by depth + coverage)

| Layer | Scope | Purpose |
|---|---|---|
| L1 exact | auth/permission/bootstrap stubs | Get past first-screen auth |
| L2 negative patch | All JSON responses | Not-logged-in code → success |
| L3 fallback | `/api` etc. not matched by L1 | Empty success body to force the UI open |

- **depth**: fake auth + `forward` rewrites business codes + `stubs`; iterate over `routes` (hash/history); produces `runtime_api.json`
- **coverage**: inject `preload.js` at **document-start** (CDP `addScriptToEvaluateOnNewDocument` or a userscript)

Verify: `window.__API_RECON_PRELOAD__` exists; business paths do not bounce back to `/login`.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — Coverage Dynamic Enumeration (Mandatory)

1. Main navigation/sidebar: click every item, wait 1–3s for network activity
2. Tabs: `role=tab`, `.ant-tabs-tab`
3. Tables: view/edit/details on the first row
4. Toolbars: export, filter, create (**avoid irreversible deletes**)
5. On entering each module: merge APIs/routes
6. SPA: controlled `pushState` for paths in `routes.txt` not yet covered (prohibited for MPA)

**Parameter trigger matrix** (mandatory): for each module record once per operation type, and **diff the multiple samples**:

| Operation | Parameters that usually appear |
|---|---|
| List first screen | Pagination + default filters |
| Click search | keyword, filter |
| Advanced filter | More optional fields |
| Create/edit | Full entity |
| Bulk/export/sort | `ids[]`, `exportType`, `sortField` |

**Under stubs, outbound bodies/headers are still real**: the request is the source of truth. Record → `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + document-start preload
- **React**: `routes.txt` + sidebar clicks + `pushState`
- **both**: 3a depth first, then 3b coverage

---

## Phase 4 — Permission Tree Reconstruction

**Trigger**: module pages are blank / each route shows only bootstrap (e.g. locale) → the content gate has not been passed.

| Symptom | Meaning |
|---|---|
| Successfully in the shell | Render gate + interceptor gate passed |
| Sidebar items missing / clicks show blank pages | Stub shape or permission codes incomplete |
| Every route has the same, very few APIs | `v-if permission` not satisfied |
| `routes.txt` is far smaller than the bundle | Must be completed from the auth module |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

Typical chain: `role_permissions` (flat codes) + `permissions/all` (tree) → `getResultTree` → `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Intermediate outputs: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

Stub checks: the outer `response_code` matches the interceptor gate; flat codes align with the tree; `routes` covers every link in `route_map`.

After updating `config.json`, **rerun Phase 3**. For large SPAs you can tune `waitUntil`, `routeTimeout`, `perRouteMs` (see reference sections A3/I).

---

## Phase 5 — Merge and Report

### Output Table

| File | Phase | Contents |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | Static bundles and paths |
| `param_candidates.json` | 1b | Static parameter field candidates |
| `config.json` | 2 | Three gates + runtime configuration |
| `runtime_api.json` | 3a | Detailed depth recording (including WS/SSE) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | Multiple samples, click log, detail |
| `route_map.json` etc. | 4 | Permission tree intermediate files (if run) |
| `params_merged.json` | 5 | Merged parameter fields + confidence |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | Routes, APIs, params, features, limitations |
| **insert_assets** | 5 | Write all service and endpoint assets into the asset store |

### 5b — Parameter Merge

Diff from `param_samples.json`; **there is no generic merge script**. For confidence rules see reference J7 (high/medium/low/pending trigger).

### 5c — Inference from Errors

Within the authorized scope you may send incomplete requests and read the 400 responses (**this is parameter recon, not vulnerability testing**): `field 'x' is required`, enum errors, etc. Watch for `data` wrappers, `variables`, and the pre-encryption `bizData`.

The report must state: runtimeMode, static/runtime API counts, parameter confidence, uncovered modules, and a summary of `CHANGES.md` relative to the reference scripts.

Suggested `site_map.json` structure:

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

## General Notes

- **Framework-agnostic**: the method is the same for webpack/Vite/Angular lazy loading
- **Transport**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web is out of scope
- **SSR**: client-side fetches can be recorded; RSC/Server Actions cannot be fully enumerated
- **Blind spots**: JSVMP, WASM, strict HMAC/mTLS validation → static + state the limitations
- **Parameter blind spots**: conditional linkage, hidden params, WASM payload assembly → "pending trigger" / "unreachable"
- **Static is the safety net**: when runtime is blocked, static can still enumerate endpoints

---

## Additional Resources

- Grep recipes, `config.json` template, troubleshooting, Hooks, section J on parameter reverse engineering, site_map template: **[reference.md](reference.md)**
- For reference script paths see the [Scripts and Gates](#scripts-and-gates) table
