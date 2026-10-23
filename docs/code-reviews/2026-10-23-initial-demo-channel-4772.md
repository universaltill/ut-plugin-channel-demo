# Review — initial demo channel plugin (ut-docs#4772, slice A)

Date: 2026-10-23. Built by Sonnet 5.5; independent security review by Fable 5.1.

## What shipped

The demo channel WASM plugin written to `sales-channel-v1`, its fake channel server (`internal/fakeshop`, test-only, never in the wasm), and the conformance suite: checks 1, 7 and 9 are implemented and self-tested against deliberately broken adapters; checks 2, 3, 4, 5, 6 and 8 skip with a TODO naming the card that unblocks them (#4765, #4762, #4767, #4771). Scripts and workflows follow `ut-plugin-import-eposnow`.

## Review result

- Manifest: `hosts: ["cloud"]`, `permissions: ["net:{account}"]` only, no secret-taking function; the wasm import audit pins imports to the channel functions plus the wasi shim.
- Orders decode no customer name, address, email or phone; the outbox never deletes and is idempotent; "already done" counts as success.
- Scripts take tokens from the environment only and never echo them; workflows match the sibling's permissions; invented data only, MIT licence.
- Verdict: safe to publish as the first commit.

## Accepted / follow-up

- Actions are pinned by major tag, as in the sibling (not SHA-pinned).
- `scripts/publish.sh` carries an internal marketplace hostname as an example, inherited from the public sibling; a placeholder in both is a later tidy-up.
- No `.golangci.yml` yet.
