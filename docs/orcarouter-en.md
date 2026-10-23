# OrcaRouter Cyber Inference

ARTEX can route its model calls through **[OrcaRouter](https://www.orcarouter.ai)**,
an OpenAI-compatible AI gateway. In ARTEX it is registered as its own provider,
**OrcaRouter Cyber Inference**, so the provider list, configuration and
documentation keep a distinct OrcaRouter identity instead of a generic
custom-endpoint field.

- Inference and model catalog: `https://api.orcarouter.ai/v1`
- Account authorization and code exchange: `https://www.orcarouter.ai`
  (`/auth`, `/api/v1/auth/keys`)
- Provider page: <https://www.orcarouter.ai/cyber>
- Cyber-tier model catalog: <https://www.orcarouter.ai/models?tier=cyber>

## Who it is for

OrcaRouter positions this provider for **security teams working on authorized,
scoped engagements** — red-team work (authorized offensive testing) and
blue-team work (defensive review, detection engineering, code review) on the
same endpoint. Access to the dedicated cyber models is limited to workspaces
that have completed OrcaRouter's business verification. Nothing in ARTEX widens
the scope of the engagement you are authorized to run; the repository's
authorized-use rules in the [README](../README.md) still apply.

## Authentication: two explicit choices

Both choices end in the same durable OrcaRouter API key (`sk-orca-…`) and are
stored in the project's existing secret store (`llm_profiles.api_key`). They
are deliberately separate: a user who already has a key never needs a browser,
and a user without one never has to copy a long-lived secret by hand.

### 1. API key (paste an existing key)

Select **OrcaRouter Cyber Inference**, leave the base URL as
`https://api.orcarouter.ai/v1`, and paste your `sk-orca-…` key into the API key
field. The key is written to the same profile store as every other provider and
is never echoed back in full — the settings page only shows the last four
characters. To rotate or remove it, overwrite the field or clear it and save.

Environment equivalent: `ORCAROUTER_API_KEY` (with `ARTEX_LLM_PROVIDER=orcarouter`).

### 2. Connect with OrcaRouter (OAuth 2.0 + PKCE)

Click **Connect with OrcaRouter**. ARTEX uses **Flow A — loopback redirect**:
the backend binds an ephemeral `127.0.0.1` port, opens the consent screen with
an S256 `code_challenge`, and receives the authorization code when you approve.
No client secret is involved and no redirect URI has to be pre-registered. The
verifier is generated fresh for every attempt from a cryptographic RNG, never
leaves the backend process, and is never logged.

On success the issued key is persisted before the UI reports success. A connect
attempt can be cancelled, times out after ten minutes, and is released when the
settings sheet closes, the page unloads, or you switch providers.

A PKCE-issued key is **durable, not a refresh token**. ARTEX reuses it until
OrcaRouter revokes it; it never invents a refresh grant. If OrcaRouter answers
`401`, the affected credential generation is marked as needing reauthentication
and only that generation is affected — a late failure from an older attempt
cannot break a newly connected key.

For self-hosted deployments, `ORCA_AUTH_BASE_URL` and `ORCA_API_BASE_URL`
override each origin independently; `ORCA_BASE_URL` sets a shared origin (the
`/v1` inference suffix is added automatically). Remote origins must be HTTPS;
plain HTTP is accepted only for loopback development addresses.

## Models

The model control is a real dropdown fed by the live, key-scoped catalog at
`GET https://api.orcarouter.ai/v1/models`, filtered by capability. It is never a
free-text field for this provider.

Recommended order:

1. **`orcarouter/auto`** — the provider default. Automatic routing, works with
   any key.
2. **Cyber-tier models for verified security teams** —
   **OrcaCyber Ultra** (`orcacyber-ultra-1.0`): security code scanning and
   multi-step reasoning over large codebases;
   **OrcaCyber Zero** (`orcacyber-zero-1.5`, also `1.0`): driven by the
   OrcaCyber harness. These appear in the picker when your workspace is cleared
   for the cyber tier; see the
   [cyber-tier catalog](https://www.orcarouter.ai/models?tier=cyber).
3. **`orcarouter/fusion`** — the general fallback.

Filters applied by the picker:

- **Chat / agent entry points** require an OpenAI, Anthropic, Gemini or
  OpenAI-Response endpoint type and exclude embedding, image-generation,
  video and rerank-only models.
- **Multimodal entry points** additionally require the model to declare the
  modality being uploaded (`image`, `audio` or `video`) in
  `architecture.input_modalities`. Models without a declared capability fail
  closed and are not offered.
- Dedicated capability pickers (`embeddings`, `image-generation`,
  `openai-video`, `jina-rerank`) only show models that declare that endpoint.

If live discovery fails, the picker shows a small verified cold-start list and
marks it as degraded; a successful live response is always authoritative and is
never merged with the fallback list. A previously selected model that is no
longer in the filtered set is cleared and replaced with the default.

## Provider evidence

- Inference is OpenAI-compatible: `POST https://api.orcarouter.ai/v1/chat/completions`
- Model list: `GET https://api.orcarouter.ai/v1/models` (Bearer auth)
- Authorization: `https://www.orcarouter.ai/auth`;
  exchange: `POST https://www.orcarouter.ai/api/v1/auth/keys`
- Console / key and authorization management:
  <https://www.orcarouter.ai/console/authorized-apps>
- Terms and legal entity: <https://www.orcarouter.ai/terms>
- Maintained by the OrcaRouter team; integration verified 2026-10-23.

> `https://api.orcarouter.ai/v1/auth/keys` does not exist. Authentication and
> inference intentionally use different origins.
