# §7 Frontend architecture

> Part 7 of the architecture document · [back to the architecture index](../architecture.md)

The Next.js static export, its source layout, API client, stores and locale handling.

#### Contents

- [7.1 Static export pipeline](#71-static-export-pipeline)
- [7.2 Source layout](#72-source-layout)
- [7.3 Route tree](#73-route-tree)
- [7.4 API client](#74-api-client)
- [7.5 Client state](#75-client-state)
- [7.6 i18n runtime as implemented](#76-i18n-runtime-as-implemented)
- [7.7 Locale contract: English and Spanish](#77-locale-contract-english-and-spanish)
- [7.8 Auth guard](#78-auth-guard)

---

### 7.1 Static export pipeline

The frontend is not a server application. It is a directory of HTML files produced by `next build` and
served by the Go binary.

| Step | What happens | Reference |
| --- | --- | --- |
| Build | `NEXT_EXPORT=1 next build` (`pnpm run build:static`) sets `output: "export"`, `images.unoptimized` and `trailingSlash` | `web/package.json:5-8`, `web/next.config.mjs:7`, `web/next.config.mjs:21-30` |
| Development | No `NEXT_EXPORT`, so `next dev` keeps hot reload and rewrites `/api/*` to `AUTOPENTEST_API` (default `http://localhost:8787`) | `web/next.config.mjs:31-43` |
| Mock mode | `NEXT_PUBLIC_MOCK=1` replaces every API call with the in-browser handler, no backend needed | `web/next.config.mjs:9`, `web/src/lib/mock/enabled.ts:1-3`, `web/src/lib/api.ts:127` |
| Embedding | The export is copied to `server/webui/dist` and compiled into the binary with `//go:embed`, build tag `embedui` | `server/webui_embed.go:16-19`, `README.md:113-117` |
| Routing | `trailingSlash: true` makes every route emit `<route>/index.html`, which is what a static file server needs | `web/next.config.mjs:28` |
| i18n plugin | `next-intl` with a request config and **no** middleware or locale path segment, so the output stays fully static | `web/next.config.mjs:44-48`, `web/src/i18n/request.ts:11-21` |

Because there is no Node process at runtime, everything that would normally be a server feature — redirects,
cookies, locale negotiation — is done on the client. [7.8](#78-auth-guard) covers the auth consequence.

### 7.2 Source layout

| Path | Contents |
| --- | --- |
| `web/src/app/(auth)/` | `login/`, `setup/` and their layout — the only pages reachable without a token |
| `web/src/app/(main)/` | The authenticated shell: `layout.tsx` (sidebar plus client auth gate), `_components/`, and the feature trees `dashboard/`, `chat/`, `function/`, `system/` |
| `web/src/app/(external)/page.tsx` | Standalone landing page |
| `web/src/components/` | Shared components: `agent-editor`, `exploration-graph`, `transcript`, `traffic-evidence-viewer`, `finding-retest-*`, `asset-dsl-search`, and `ui/` (the shadcn/base-ui primitives) |
| `web/src/lib/` | `api.ts` (the typed backend client), `types.ts` (DTOs), `auth.ts` (token storage), `agent-i18n.ts`, `status.ts`, `utils.ts`, `mock/`, `preferences/` |
| `web/src/stores/preferences/` | The zustand theme/layout store and its provider |
| `web/src/i18n/` | `config.ts`, `request.ts`, `locale-store.ts`, `intl-runtime-provider.tsx`, `datetime.ts` |
| `web/src/navigation/sidebar/` | The sidebar catalogue (two groups: Features, System) |
| `web/src/scripts/` | `extract-i18n.ts`, `check-i18n-keys.ts` and the i18n key tests |
| `web/messages/` | `en.json` (source) and `es.json` (the only translation) |

### 7.3 Route tree

27 page modules exist under `web/src/app`:

| Route | Page | Sidebar entry |
| --- | --- | --- |
| `/` | `(external)/page.tsx` | — |
| `/login`, `/setup` | `(auth)/login/page.tsx`, `(auth)/setup/page.tsx` | — |
| `/dashboard` | `(main)/dashboard/page.tsx` | Dashboard |
| `/chat` | `(main)/chat/page.tsx` | Chat |
| `/function/tasks`, `/function/tasks/detail` | `(main)/function/tasks/page.tsx`, `…/tasks/detail/page.tsx` | Tasks |
| `/function/findings`, `/function/findings/detail` | `(main)/function/findings/page.tsx`, `…/findings/detail/page.tsx` | Findings |
| `/function/traffic` | `(main)/function/traffic/page.tsx` | Traffic |
| `/function/commands` | `(main)/function/commands/page.tsx` | Tool runs |
| `/function/llm-records` | `(main)/function/llm-records/page.tsx` | LLM records |
| `/function/assets` | `(main)/function/assets/page.tsx` | Assets |
| `/function/sync` | `(main)/function/sync/page.tsx` | Asset sync |
| `/function/workspace` | `(main)/function/workspace/page.tsx` | Workspace |
| `/system/llm` | `(main)/system/llm/page.tsx` | LLM |
| `/system/agents`, `/system/agents/detail` | `(main)/system/agents/page.tsx`, `…/agents/detail/page.tsx` | Agent |
| `/system/mcp` | `(main)/system/mcp/page.tsx` | MCP |
| `/system/skills` | `(main)/system/skills/page.tsx` | Skill |
| `/system/tools` | `(main)/system/tools/page.tsx` | Tools |
| `/system/notify` | `(main)/system/notify/page.tsx` | Notifications |
| `/system/intercept`, `…/assets`, `…/approvals` | `(main)/system/intercept/{page.tsx,assets/page.tsx,approvals/page.tsx}` | Intercept rules, Asset blocking, Approvals |
| `/system/logs` | `(main)/system/logs/page.tsx` | Logs |
| `/system/settings` | `(main)/system/settings/page.tsx` | System settings |

The detail pages are separate modules rather than dynamic segments because a static export cannot resolve a
runtime path parameter: `/function/tasks/detail?id=…` is a query string, not a route parameter.

### 7.4 API client

`web/src/lib/api.ts` is the only place that talks to the backend. Every screen goes through it; there is no
second fetch wrapper.

| Element | Behaviour | Reference |
| --- | --- | --- |
| `http<T>` | Single `fetch('/api' + path)` wrapper: adds `Content-Type` when there is a body, adds `Authorization: Bearer` from `localStorage`, clears the token and redirects to `/login` on 401, throws `ApiError` on any other non-2xx, returns `undefined` on 204 | `web/src/lib/api.ts:126-162` |
| `ApiError` | Carries the HTTP status so screens branch on status, never on the message text | `web/src/lib/api.ts:111-119` |
| `sseUrl` | Builds a same-origin stream URL in production and the `:8787` origin in dev, appending `?token=` because `EventSource` cannot send headers | `web/src/lib/api.ts:201-212` |
| `api` object | One method per endpoint, typed against `lib/types.ts`, grouped by area in the same order as the Go route table | `web/src/lib/api.ts:247-600` |
| Normalisation | `arr()` coerces Go's `nil` slice (JSON `null`) to `[]`; `tq()` and the filter serialisers keep query-string construction in one place | `web/src/lib/api.ts:236-244` |
| Mock seam | `MOCK` short-circuits `http` into `mockHandle`, so the demo runs with no backend | `web/src/lib/api.ts:127`, `web/src/lib/mock/handler.ts` |

### 7.5 Client state

| Store | Kind | Scope | Reference |
| --- | --- | --- | --- |
| Preferences | zustand (`createPreferencesStore`) for theme mode/preset, layout, navbar style, sidebar variant/collapsible and font; hydrated from cookies so the prerendered HTML already carries the right `data-*` attributes | Global | `web/src/stores/preferences/preferences-store.ts:29`, `web/src/stores/preferences/preferences-provider.tsx:46-130` |
| Display locale | Custom external store (`useActiveLocale` over `useSyncExternalStore`) backed by `localStorage` plus a cookie, with same-tab fan-out and cross-tab `storage` events | Global | `web/src/i18n/locale-store.ts:20-59` |
| Everything else | Per-page `useState`/hooks (`use-side-questions`, `use-mobile`), refetched from the API | Page | `web/src/hooks/` |

There is no global data cache and no server-state library: each page fetches what it renders, and the pages
that must stay live subscribe to an SSE stream and merge events into local state
(`web/src/lib/activity-merge.ts`).

### 7.6 i18n runtime as implemented

`next-intl` is used as a message formatter, not as a routing layer.

| Stage | Implementation | Reference |
| --- | --- | --- |
| Build-time locale | `resolveLocale()` reads only `NEXT_PUBLIC_LOCALE` (never `cookies()`/`headers()`), so prerendering stays static | `web/src/i18n/config.ts:33-45` |
| Server render | `getRequestConfig` loads exactly one message file through an explicit import map (no directory context) and returns it as the request locale | `web/src/i18n/request.ts:5-21` |
| Client switch | Both message bundles are imported statically and handed to `NextIntlClientProvider` with the locale from the store, so a switch re-renders immediately — no reload, no async load | `web/src/i18n/intl-runtime-provider.tsx:11-44` |
| `<html lang>` | Seeded by the root layout from the build locale, then corrected on the client from the store | `web/src/app/layout.tsx:36-38`, `web/src/i18n/intl-runtime-provider.tsx:30-36` |
| Persistence | `localStorage` plus the cookie `artex_locale`, one-year cookie; the cookie currently has no server reader and exists for a future server-side consumer | `web/src/i18n/config.ts:26-29`, `web/src/i18n/locale-store.ts:53-59` |
| Key parity | `check-i18n-keys` and the vitest suite assert that `en.json` and `es.json` have identical leaf keys; the CI web job runs it as a gate | `web/src/scripts/i18n-keys.ts`, `web/package.json:8`, `.github/workflows/web.yml:60` |

The display language and the agent output language are separate switches: the UI locale never leaves the
browser, while the output language is a server setting (`output_language`) that also drives `langDirective`
and the backend message catalogue (`server/manager.go:282-287`, `agent/prompt.go:77`).

### 7.7 Locale contract: English and Spanish

| Element | Value | Reference |
| --- | --- | --- |
| Supported locales | `LOCALES = ["en", "es"] as const` | `web/src/i18n/config.ts:3` |
| Default | `DEFAULT_LOCALE: Locale = "en"` — English is the source language, every key must exist there first | `web/src/i18n/config.ts:7-12` |
| Picker labels | `LOCALE_LABELS = { en: "English", es: "Español" }`, deliberately untranslated | `web/src/i18n/config.ts:14-18` |
| Storage keys | `LOCALE_COOKIE = LOCALE_STORAGE_KEY = "artex_locale"` | `web/src/i18n/config.ts:26-29` |
| Validation | `isLocale(raw)` narrows any unknown value; anything else falls back to `resolveLocale()` | `web/src/i18n/config.ts:31-35` |
| Message files | `web/messages/en.json` and `web/messages/es.json` only; no loader references a removed bundle | `web/src/i18n/request.ts:5-9`, `web/src/i18n/intl-runtime-provider.tsx:13-16` |
| Backend language | `config.ActiveLanguage()` resolves `settings.output_language` over `config.Language()` (`ARTEX_LANG` > config file > `en`); only `en`/`es` are accepted | `config/config.go:66-96`, `server/manager.go:282-287` |

Adding a third locale would need an entry in `LOCALES`, a bundle in `messages/`, an import in both the
request loader and the runtime provider, and the label map — four places, all checked by the `Locale` union.

### 7.8 Auth guard

Static export disables Next middleware, so the guard is split in two:

| Guard | Runs where | Behaviour | Reference |
| --- | --- | --- | --- |
| Cookie redirect | `web/src/proxy.ts`, active only in builds that have a server (dev / `next start`); the export ships no middleware by design | No `artex_token` cookie and not on `/login`/`/setup` → redirect to `/login`; a token on an auth page → redirect to `/function/tasks` | `web/src/proxy.ts:1-32`, `web/next.config.mjs:44-45` |
| Client gate | `(main)/layout.tsx` in every build | Reads `auth.getToken()` in an effect and renders nothing until a token is confirmed, so no protected UI or API call flashes for a signed-out visitor | `web/src/app/(main)/layout.tsx:36-56` |
| Token storage | `localStorage.artex_token` plus the `artex_token` cookie (7 days, `SameSite=Lax`), cleared together | | `web/src/lib/auth.ts:1-30` |
| API-side backstop | A 401 from any call clears both and navigates to `/login` | | `web/src/lib/api.ts:137-145` |

The token is never verified client-side: `auth.getCurrentUser()` decodes the JWT payload for display only,
and the source comment says so (`web/src/lib/auth.ts:32-40`). Authorisation is enforced by `requireAuth` in
the Go server ([6.3](http-api.md#63-authentication-model)).

---

← [architecture index](../architecture.md) · section numbers are stable across the split (§7.x).
