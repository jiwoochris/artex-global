# api-recon — reference manual

Grep recipes, the `config.json` template, and troubleshooting. All greps run against the `js/`
directory. When a bundle is a single long line, run `js-beautify` or `sed 's/}/}\n/g'` first; a raw
grep with a context window usually works as is.

## Script notes

Every file under `scripts/` is a **reference template** and must be adapted to the target site before
running. Typical changes:

| Script | What usually needs changing |
|---|---|
| `harvest_static.py` | Endpoint regex, webpack/Vite manifest parsing, micro-frontend publicPath, retry/concurrency |
| `runtime_harvest.js` | neutralize field names and success values, stub matching rules and body structure, route source, WS recording, `waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, whether L3 is enabled, `recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` destructive links, cookie, depth/max, same-domain filtering |
| `extract_route_map.py` | `routeMap` / `routeLink` regexes, KEY naming patterns |
| `build_perm_tree.py` | `userRouteAuth` parsing, `ROOTS`/`PREFIX_PARENT` hierarchy heuristics, stub outer field names |
| `config.json` | The single entry point for all the site-specific parameters above |

Put the adapted files in the task working directory (e.g. `recon/`), and note in the report the
specific changes relative to the reference scripts.

---

## A. Reversing the three gates

### A1. Render gate — "how does it decide you are logged in?"

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

Find the chain `isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))`, and determine the
**storage key**, the **container** (Cookie vs localStorage), and the **encoding**:

| Encoding | How to forge it in config |
|---|---|
| Plain string / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | Unsigned/`alg:none` JWT, or sign with a key found in the bundle |
| Encrypted (SM2/AES/RSA) | Find the hard-coded key; when the render gate only needs a decodable blob you can forge it; otherwise fall back to static analysis |

→ Write it into `cookies` / `localStorage`.

### A2. Interceptor gate — "what triggers the redirect to /login?"

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(unauthorized|not logged in|please sign in again|session expired|login expired|authorization|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

Determine: the **field name**, the **success value** (usually `0` or `200`), and the **failure value**
that triggers the redirect. Verify with a junk session:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ Write it into `neutralize.fields` + `neutralize.success`.

### A3. Content gate — "where do the menu and permissions come from?"

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**Two layers of data** (common in enterprise back offices):

| API | Typical payload | Consumer |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | Route guards, button-level ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | Sidebar menu rendering |
| `userRouteAuth` inside the bundle | `{ CODE: { url, name? } }` | code → frontend path |
| `routeMap` inside the bundle | `{ KEY: { name, link } }` | Alias resolution (webpack `o.DASHBOARD`) |

Read the consumer code to confirm: how `getResultTree(tree, permissions)` filters, and which field
`v-if` / `hasAuth(code)` checks.

**Manual forge** (small sites): build a permissive payload → `stubs`.

**Full permission-tree reconstruction** (large sites where the sidebar or sub-modules are still blank):
see **section I**.

---

## B. config.json template

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

Field notes:
- `runtimeMode`: `depth` (Puppeteer), `coverage` (browser MCP), `both`
- `cookies[].value` prefixes: `b64json:` → base64(JSON); `json:` → raw JSON; no prefix → literal
- `forward: true` forwards the real request and rewrites the code fields; `false` stubs fully offline
- `mockTier`: the level enabled by the preload in coverage mode, e.g. `L1+L2`, `L1+L2+L3`
- `routes` comes from `routes.txt`; after the menu is forged the harness appends `<a href>` targets
- `captureResponses` / `recordWs` only work in depth mode
- `waitUntil`: use `domcontentloaded` for large SPAs, to avoid `networkidle2` hanging
- `routeTimeout`: `page.goto` timeout per route (milliseconds)
- `proxy`: Puppeteer `--proxy-server`; `HTTP_PROXY` / `HTTPS_PROXY` also work

### B1. Two-stub template (role_permissions + permissions/all)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

The outer field names (`response_code` / `code` / `data`) must match the A2 interceptor gate, and
`permissions` must cover every leaf code in the tree.

---

## C. coverage mode: preload configuration

Edit the `CONFIG` object at the top of `scripts/preload.js`, or replace it before injecting through
CDP:

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* same as config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

Verify: `window.__API_RECON_PRELOAD__ === true` and the pathname stays stable.

Export the recording:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime hook capabilities

The browser hook capabilities built into preload (coverage) and runtime_harvest (depth), and what
they cover:

| Hook capability | Value for API discovery | Covered |
|---|---|---|
| Hook fetch / XHR.open | Record request URL/method | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | Discover headers such as Authorization | ✅ `observe.xhrHeaders` |
| Hook localStorage/cookie reads | Confirm session key names | ⚠️ optional `observe.storageReads/cookieReads` |
| Get Vue routes | Completes frontendRoutes | ✅ `__API_RECON_ROUTES__` (already-loaded routes) |
| Neutralize Vue route guards / block login redirects | Opens modules so their APIs fire | ✅ `neutralizeVueRouter` + native navigation neutralization |
| Get React routes | Complete routes | ⚠️ static + clicking; no dedicated hook |
| Block page navigation (login path) | Stay on the page to analyze | ⚠️ only login paths are blocked, so business navigation is not blocked |
| Hook crypto libraries (CryptoJS/SM, etc.) | Encrypted parameters → plaintext API body | ❌ you must hook the encryption function's inputs by hand; record the finding in config |
| Anti-debugging bypass | Otherwise runtime records no APIs | ❌ must be handled by hand; static analysis still works |

---

## E. Endpoint extraction regex (when the static result is too small)

Relax `extract_endpoints` in `harvest_static.py`, or do it manually:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. Troubleshooting

| Symptom | Cause → action |
|---|---|
| Few static APIs | Endpoint dialect mismatch → relax the regex (section E) |
| Chunk count ≪ manifest | CSS-only or undeloyed chunks; the 404 was already retried |
| Runtime still shows the login page | Wrong render gate → re-check A1: key name, container, encoding, domain |
| Inside the shell but modules blank | Content gate → forge the menu (A3); the `routes` paths may be wrong |
| Every route only gets bootstrap/locale | Incomplete permission codes → permission-tree reconstruction in section I; check the `role_permissions` + `permissions/all` double stub |
| Sidebar entries present but sub-pages blank | The tree lacks intermediate nodes, or the codes disagree with `userRouteAuth` |
| Every API redirects to login | Interceptor gate → confirm `neutralize`; nested fields need the walk logic extended |
| Zero WS frames | Subscription happens only after user interaction; increase `perRouteMs` |
| Empty response body | Real responses only exist with `forward: true` |
| Chromium missing | Install chromium or set `config.chromium` / `CHROMIUM` |
| Still bounced to login despite many mocks | Hook installed too late or the `location.href` setter is missing → document-start + preload |
| All lists empty | Empty arrays from L3 are expected; keep clicking tabs/settings/details |
| Mistaking a Redux action for a route | Filter internal paths containing get/set/change/clear/toggle/upload |
| Vue still redirects to login | preload not at document-start → change the injection timing; or clear guards manually when `neutralizeVueRouter: false` |
| URLs appear in responses but not in the log | Enable `extractUrlsFromResponse`; or extract manually from `__API_RECON_DETAIL__` |
| Unknown Authorization header name | Enable `observe.xhrHeaders` or inspect request headers in DevTools |
| Runtime very slow / timing out | Switch to `waitUntil: domcontentloaded`; lower `routeTimeout`; do not use `networkidle2` |
| Proxy connection fails | Check `proxy` / environment variables; Puppeteer and curl must use the same proxy port |

---

## G. Hardened targets

When the server validates the session step by step (signed cookies that cannot be forged, menus that
are server-rendered and cannot be stubbed), runtime stalls at the shell. Expected behavior:

- **Static analysis is enough to enumerate endpoints** — the module paths are in the code
- If authorization allows it, run the same harness with a **real session**: `forward: true`, no
  neutralize needed, capturing the real methods/params/responses

---

## H. Checklist for a single task

1. Confirm the authorized scope
2. **Read** `scripts/harvest_static.py` → adapt it to the target → run it → review `api_static.txt`,
   `routes.txt`
3. **Phase 1b**: path anchor window expansion + binding layer → `param_candidates.json` (section J)
4. Reverse A1/A2/A3 → write the site-specific `config.json`
5. **Read and adapt** `runtime_harvest.js` / `preload.js` before running them
6. `runtimeMode=depth`: `npm install` → run the adapted harvest script
7. `runtimeMode=coverage/both`: inject the adapted preload at document-start → browser MCP dynamic
   enumeration + **parameter-trigger matrix**
8. Modules do not render → **permission-tree reconstruction in section I** → patch the stubs → re-run
9. Multi-sample parameter diff + error-driven inference → `params_merged.json`
10. Merge → `site_map.json` + `api_merged.txt`, honestly stating coverage, gaps, and script changes

---

## I. Permission-tree reconstruction (Phase 4 in depth)

Use this when forging a simple `menus: [{ path, show: true }]` does not work and sub-modules still do
not mount.

### I1. Locate the auth module

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

Record: the **permission API path**, the **response field names**, and the **consuming chunk file
names**.

### I2. Extract routeMap

```bash
python3 scripts/extract_route_map.py recon/js recon/
# produces recon/route_map.json
```

If it reports `[!] no routeMap pattern found`: relax the regex in `extract_route_map.py`, or grep
manually:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. Build the permission tree + stubs

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Script logic:
1. Parse `userRouteAuth={MONITOR:{url:...},...}` (including webpack aliases such as `He=o.DASHBOARD`)
2. Resolve aliases through `route_map.json` → real paths
3. Infer the parent from the code prefix (`MONITOR_ALERT` → `MONITOR`)
4. Output `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json`
5. With `--config`, write the `stubs` into `config.json` and extend `routes` automatically

**Adapt to the target** (at the top of the script):
- `DEFAULT_ROOTS`: the list of top-level module codes
- `DEFAULT_PREFIX_PARENT`: the `PREFIX_` → parent mapping
- `DEFAULT_EXTRA_PARENT`: orphan nodes that have no prefix relationship

### I4. Validate stub consistency

```bash
# the number of permissions should be ≈ the number of userRouteAuth entries
wc -l recon/perm_codes_all.txt
# routes should cover every link in route_map
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. Re-run runtime and compare

```bash
node recon/runtime_harvest.js recon/config.json
# compare the runtime_api.json count before and after the forge; check whether module APIs appear for /attack, /asset, etc.
```

| Before the forge | After the forge (success) |
|---|---|
| The same 3–5 bootstrap calls on every route | Different routes trigger different module APIs |
| Only `/api/locale/language` | `/api/web/...` module endpoints appear |
| `routes.txt` has a handful of routes | `routes` has 80–110+ entries from route_map |

### I6. When it still fails

- **coverage mode**: click the sidebar + tabs; permission gating may only request after interaction
- **stub fields**: compare the nesting of a real API (curl + real session) with the stub
- **extra guards**: grep for button-level checks such as `hasPermission|checkRole|func.` and extend
  `role_permissions.permissions`
- **static fallback**: module API paths are still in `api_static.txt`, and runtime only adds
  METHOD/body; keep the parameters from `param_candidates.json` and the recorded samples

---

## J. Parameter reversing (Phase 1b / 5b / 5c)

**A methodology, not a universal script.** Find paths with regex; find parameters with anchor window
expansion + the UI binding chain + multi-sample diff + error-driven inference.

### J1. Anchor window expansion — find the packing object from a path

```bash
# use a path known from Phase 1 as the anchor
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. Wrapper layers and transport shapes

```bash
# axios / unified request
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# path parameters
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. Validation gate — required / format / enum

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. Binding layer — form → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

Runtime fill-in: DevTools → Network → request → **Initiator** (call stack); walk up from
`fetch`/`send` to the packing function.

### J5. Encrypted parameters

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**Do not guess fields from the ciphertext** — hook the encryption function's **inputs** and record the
plaintext payload before encryption; write the finding into `config.json` / `param_candidates.json`.

### J6. Parameter-trigger matrix (mandatory in Phase 3)

Record each module once per operation and diff the request body/query:

| Operation | Watch for |
|---|---|
| List first page | Pagination defaults |
| Search | keyword, filters |
| Advanced filter | optional fields |
| Create/edit | The full entity |
| Bulk/export | `ids[]`, `exportType` |
| Sort/paging | `sortField`, `order` |

Output `param_samples.json`: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. Confidence rules

| Confidence | Condition |
|---|---|
| **High** | Static call site + at least 2 consistent runtime samples |
| **Medium** | Static only, or only 1 runtime sample |
| **Low** | Inferred from a response or an error, not verified twice |
| **Pending trigger** | Field known statically, but UI/permissions never reached it |

### J8. Quick recipes per scenario

| Scenario | Order |
|---|---|
| REST list page | J1 packing object → J6 four diffs → J3 rules |
| Create/edit form | J3 Form name → J4 submit chain → runtime submit + deliberately leave fields empty to see the 400 |
| GraphQL | J2 variables declaration → record variables per operation at runtime |
| Encrypted body | J5 hook inputs → the fields before encryption are the real params |

### J9. Mapping to the api-recon phases

| api-recon | Parameter recon |
|---|---|
| Phase 1 static | J1 anchor window expansion |
| Phase 2 A2 interceptor | Globally injected fields (tenantId, sign) |
| Phase 3 runtime | J6 trigger matrix + `param_samples.json` |
| Phase 4 permission tree | Different modules have different forms → only enough permission triggers all fields |
| Phase 5 merge | `params_merged.json` + confidence; never decide required/optional from one sample |

### J10. Troubleshooting

| Symptom | Action |
|---|---|
| A field name is in the static code but never appears at runtime | Mark it "pending trigger"; complete the permission tree / click advanced filters / trigger every option of a linked select |
| The same path has different body shapes | Normal — record one entry per `action`; do not force a single merged schema |
| The stub response is fake but you want the params | **Look at the outbound request** body/headers; never infer from the stub response |
| A 400 names a nested field | Watch the outer wrapper `data`/`bizData`/`variables` |
| GraphQL shows only operation names | Expand the `variables` JSON; find `$var: Type` statically |

---
