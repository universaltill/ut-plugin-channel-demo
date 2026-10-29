# ut-plugin-channel-demo — rules for working in this repo

The demo **sales-channel** adapter and the home of the conformance suite
(ADR-0169, ut-docs#4772; contract `ut-docs/reference/contracts/sales-channel-v1.md`).
WASM `channel` plugin (ADR-0009 one-repo-per-plugin, ADR-0001) in Go on the
official guest SDK `github.com/universaltill/universal-till/sdk/plugin/channel`
— never hand-rolled `//go:wasmimport` lines. It runs only in the ut-cloud
channel host (`hosts: ["cloud"]`); a till lists it and refuses to run it.

- **No secret, no client id, no real shop data**, ever (ADR-0169 section 5; the
  repo is public). The shop's token and secret in `internal/fakeshop` are
  invented constants. Never point a test at a real shop.
- **The plugin never holds a credential or its own account.** Network is
  `channel.HTTP` only, with URLs of the form `https://{account}/...`; the host
  fills the account in, adds the credential and strips it from answers. Never
  add a second network path, a `DELETE`, or anything that reads a secret.
- **Ids that reach a URL** pass `okID` and `url.PathEscape`; values travel as
  JSON bodies, never interpolated. Image URLs are kept only on the channel's
  own domain.
- **No customer name, address, email or phone** in a submitted `order`, in a
  cursor, a log field or an ack. Contact details exist only in `contact` answers
  and are never stored. The customer struct is decoded for id and country only.
- **Records are deterministic** (`external_version` = the channel's
  `updated_at`) so the host's idempotency gives "zero changes" on a second run.
- **The orders record is the contract's** (`orders.go` `orderRecord`), not the
  SDK's thinner `channel.Order`.
- **Per-ask budgets** stay well inside the host caps (2,000 HTTP calls, 20,000
  records): `maxProductPages`, `maxOrderPages`. A budget-limited ask resumes
  from its cursor.
- **`AccountSuffix` in `src/app/api.go` equals `channel.account_suffix`** in
  `manifest.json` (`scripts/validate.sh` checks it).
- **The suite must be able to fail:** every check in `conformance/suite.go` has a
  deliberately broken adapter in `conformance/selftest_test.go`. TODO checks name
  the card they wait for and `t.Skip`; never delete one or make it pass vacuously.
- **Tests run natively on the SDK `FakeHost`** behind `conformance.Emulator`
  (`*_test.go` are `//go:build !wasip1`). TDD: failing test first.

## Before committing

`gofmt -l .` (no output), `go vet ./...`, `GOOS=wasip1 GOARCH=wasm go vet ./...`,
`scripts/build.sh`, `go test ./...`, `scripts/validate.sh`, `scripts/package.sh`,
`shellcheck scripts/*.sh`, `scripts/check-version-bump.test.sh`. Feature branch;
review record in `docs/code-reviews/<date>-<topic>.md`; then merge.

## Releases

Release = tag `v<version>` matching `manifest.json`, cut by `auto-tag-release.yml`
on merge. **Any PR that changes what ships — `manifest.json`, `README.md`,
`LICENSE`, anything under `src/`, `go.mod`/`go.sum`, `scripts/build.sh` or `scripts/package.sh` — must
bump `manifest.json`'s `version` in the same PR** (ut-docs#1940), enforced by
`scripts/check-version-bump.sh`. The marketplace signs the release at approval;
the plugin repo holds no signing key.
