<!--
  This PR template is specific to artex-global (the ARTEX global edition).
  Please read CONTRIBUTING.md first for the contribution and localization policy.
-->

## Summary

<!-- One or two sentences on what changes and why. -->

## Type of change

<!-- Put an x in the applicable items. -->

- [ ] Bug fix (`fix`)
- [ ] New feature (`feat`)
- [ ] Documentation (`docs`)
- [ ] Localization / translation (`i18n`)
- [ ] Refactoring / cleanup (`refactor` / `chore`)
- [ ] Other:

## Related issues

<!-- e.g. Closes #123 -->

## How it was verified

<!-- Which commands you ran, and what you observed. Attaching the output helps. -->

- [ ] Go change: `go build ./...` · `go vet ./agent/` · `go test ./agent/` pass
- [ ] web change: `npm run check` · `npm run build` pass
- [ ] UI change: screenshots attached

## Localization checklist

<!-- Leave this section empty for changes unrelated to localization. -->

- [ ] I did **not** translate the agent's **internal reasoning prompts (the behavior-instruction
      body, `agent/promptcatalog.go` and `agent_prompts`)**. (Performance is preserved.)
- [ ] I only touched the user-facing output (detection findings, summaries, reports, chat replies) in
      the selected language.
- [ ] Commands, payloads, code, URLs, and raw logs were left untranslated.
- [ ] UI strings are not hard-coded; they were added as keys to `web/messages/en.json` (source) and
      `web/messages/es.json` (translation).

## Authorized use

- [ ] This change does not encourage use outside the
      [authorized scope](../README.md#️-read-first--authorized-use-and-legal-notice) or beyond
      applicable law. Verification was performed only against owned/authorized targets or in a local
      isolated environment.
- [ ] Test and scan artifacts are not included in the commit.
