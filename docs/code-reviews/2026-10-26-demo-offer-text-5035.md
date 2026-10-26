# Code review — demo channel offer text (ut-docs#5035)

- **Date:** 2026-10-26
- **Branch:** `docs/5035-demo-offer-text`
- **Author:** build routine `:41` (lane:cloud-41)
- **Reviewer:** independent subagent, different model from the author

## Change

Since ut-docs#5025 ut-cloud offers the demo channel only where
`UT_CHANNEL_DEMO=1` (local, CI or demo runs) and to no shop in production,
test shops included (`internal/channelconn/marketplace_registry.go`,
`types.go`, `service.go`, router `DemoEnabled`). The README and the
manifest description (marketplace copy) still said it is "offered to test
shops". Both now state the real rule. `manifest.json` version 0.2.0 → 0.2.1
(text-only patch; ut-docs#1940 version-bump rule).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | Manifest wording "in demo and test setups" could read as including test shops, which do not see it; jargon for a merchant. | Fixed: "is for demonstrations and automated testing, and is never offered to any shop in production, test shops included." |
| 2 | nit | README sentence "never to a shop in production, test shops included" parsed ambiguously. | Fixed: split into "No shop is offered it in production, test shops included." |
| 3 | should-fix | Review record missing. | This file. |

Wording checked against the ut-cloud code; no other stale "test shop"
wording in the repo (history in `docs/code-reviews/` excluded).

## Verification

`gofmt -l .` empty, `go vet ./...` and `GOOS=wasip1 GOARCH=wasm go vet ./...`
clean, `scripts/build.sh`, `go test ./...`, `scripts/validate.sh`
(`ok com.universaltill.channel-demo v0.2.1`), `scripts/package.sh`,
`scripts/check-version-bump.test.sh` pass. `shellcheck` is not installed
in this session; no script changed — CI runs it.
