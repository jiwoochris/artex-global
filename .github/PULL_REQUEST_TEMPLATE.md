<!--
  This PR template is specific to artex-global (the ARTEX global edition).
  Please read CONTRIBUTING.md first for the contribution policy and localization principles.
-->

## Summary

<!-- Describe what you are changing and why, in one or two sentences. -->

## Type of change

<!-- Put an x in the boxes that apply. -->

- [ ] Bug fix (`fix`)
- [ ] New feature (`feat`)
- [ ] Documentation (`docs`)
- [ ] Localization / translation (`i18n`)
- [ ] Refactoring / cleanup (`refactor` / `chore`)
- [ ] Other:

## Related issues

<!-- e.g. Closes #123 -->

## How it was verified

<!-- Describe which commands you ran yourself and what they confirmed. Attaching the result logs helps. -->

- [ ] Go changes: `go build ./...` · `go vet ./agent/` · `go test ./agent/` pass
- [ ] web changes: `npm run check` · `npm run build` pass
- [ ] UI changes: screenshots attached

## Localization checklist

<!-- You may leave this section empty if the PR is unrelated to localization. -->

- [ ] I did **not translate the agent's internal reasoning prompts (the behavior-instruction body, `agent/promptcatalog.go` · `agent_prompts`).** (Preserves performance.)
- [ ] I only localized user-facing output (detection findings, summaries, reports, chat responses) into the selected language.
- [ ] I left command, payload, code, URL, and log originals as they are, without translating them.
- [ ] I did not hard-code UI strings; I added them as keys in `web/messages/` (Korean in `web/messages/ko.json`) and preserved the original in `web/messages/zh.json`.

## Authorized-use confirmation

- [ ] This change does not encourage use beyond the [authorized-use scope](../README.md#️-read-first--authorized-use-and-legal-notice)
      and applicable law. I ran verification only against targets I own or am authorized to test, or in a
      local isolated environment.
- [ ] I did not include test or scan output in the commits.
