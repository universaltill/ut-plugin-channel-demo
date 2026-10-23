# ut-plugin-channel-demo

The **demo sales channel** for Universal Till (`com.universaltill.channel-demo`).
A pretend web shop that lets anyone try a sales channel without a Shopify (or any
other) account: connect it, import its products, receive its orders and send
stock, fulfilment and cancels back. Every product, order and customer is invented.

It is a signed **WASM plugin of type `channel`** that runs only in the ut-cloud
channel host, never on a till (ADR-0169, contract
`ut-docs/reference/contracts/sales-channel-v1.md`). It is offered to test shops
(and wherever `UT_CHANNEL_DEMO=1`), never to a real merchant in production.

It is also the **reference adapter** and home of the **conformance suite**
(contract section 6) that every channel adapter runs in CI.

## What it does

| Ask | Behaviour |
|---|---|
| `channel.connect.test.ask` | Reads the shop's name, scopes, locations and counts. Writes nothing. |
| `channel.oauth.callback.ask` | Parses the install parameters the host already verified; checks the shop domain; returns account and scopes. The host does the code exchange, PKCE, refresh and the callback signature check; the plugin never sees a token or a secret. |
| `channel.webhook.ask` | Parses a webhook the host already verified. No tenant context: only `log` works. |
| `channel.import.ask` | Pages through products with variants and stock, submits `listing` and `stock_observed` records, resumable through the `import.products` cursor; records are deterministic so a second run is byte-identical. |
| `channel.fetch.ask` / `channel.tick` | Re-fetch named orders / pull orders updated since the `orders.since` cursor; submit contract `order` records with **no customer name, address, email or phone**. |
| `channel.contact.ask` | Fetches an order's contact details live for display; never stored, submitted or logged. |
| `channel.outbox.ask` | `stock.set` (absolute, compare-and-set on `expect`, a stale `expect` reports the channel's value and pulls recent orders), `fulfilment.create`, `order.cancel` (both idempotent on the operation id; "already done" is success). Anything else, a delete included, is dead-lettered. |

The plugin has **no account name, no credential and no network of its own**. Its
only network path is `channel_http`; it writes `https://{account}/...` and the
host fills in the connection's verified account and adds the credential header.

## Layout

```
manifest.json            channel block, hosts ["cloud"], permissions ["net:{account}"]
src/                     main.go (channel.Run) and app/ (the asks)
internal/fakeshop/       the invented channel server (OAuth, products, inventory,
                         orders, fulfilment, HMAC webhooks, throttling, hostile modes)
conformance/             the suite (Run) and the host emulator it runs an adapter on
scripts/                 build, validate, package, publish, approve
```

## Develop

```
go vet ./... && GOOS=wasip1 GOARCH=wasm go vet ./...
scripts/build.sh                 # bin/plugin.wasm
go test ./...                    # unit tests on the SDK's FakeHost + the conformance suite
scripts/validate.sh && scripts/package.sh
```

Tests run natively against `internal/fakeshop` behind the conformance emulator
(egress rules, credential injection and redaction, 429 retry, per-call caps).
The emulator is **not** the cloud sandbox; the sandbox's own limits are pinned
by ut-cloud `internal/channelhost` (`TestEgress_*`, `TestHostile_*`).
ut-cloud also has `TestDemoPluginRunsInTheRealSandbox` (set `UT_DEMO_PLUGIN_DIR`
to this directory) that runs the built module and this manifest through the real
host.

## The conformance suite

`conformance.Run(t, conformance.Target{...})` runs the nine checks of contract
section 6 against an adapter and **its own** fake server. Implemented now: check 1
(connect test reads and writes nothing), check 7 (no outbox operation deletes)
and check 9 (a hostile channel cannot make the adapter leave `{account}`, leak
the credential or overrun the caps). The rest are marked TODO and skip with the
card they wait for (ut-docs#4762 webhooks, #4765 catalogue sync, #4767 orders,
#4771 write-back). `selftest_test.go` runs the checks against deliberately broken
adapters, so the suite is known to fail when it should.

## Release

A release is the tag `v<manifest version>`, cut on merge to `main`. The workflow
packages `dist/<id>_<version>_universal.tar.gz`, uploads it to the marketplace
(`scripts/publish.sh`) and, on the dev marketplace, approves it (`scripts/approve.sh`),
which is where the marketplace **signs** it. Any change to what ships must bump
`manifest.json`'s `version` (`scripts/check-version-bump.sh`).

## Security

No secret, client id or real shop data lives in this repo (ADR-0169 section 5):
the OAuth app credentials of a connection are entered by the merchant in my. and
sealed per tenant in the cloud. The demo shop's token and secret in the tests are
invented constants.

MIT licensed, see `LICENSE`.
