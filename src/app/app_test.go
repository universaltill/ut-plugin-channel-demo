//go:build !wasip1

package app_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/sdk/plugin/channel"

	"github.com/universaltill/ut-plugin-channel-demo/conformance"
	"github.com/universaltill/ut-plugin-channel-demo/internal/fakeshop"
	"github.com/universaltill/ut-plugin-channel-demo/src/app"
)

// All data is invented. Tests run natively on the SDK's FakeHost behind the
// conformance emulator (egress rules, credential injection, 429 retry).

type env struct {
	t    *testing.T
	shop *fakeshop.Shop
	em   *conformance.Emulator
}

func newEnv(t *testing.T, opt fakeshop.Options) *env {
	t.Helper()
	shop := fakeshop.New(opt)
	em := conformance.NewEmulator(t, app.Handlers(), shop.Handler(), shop.Account(), fakeshop.Header, shop.Token())
	return &env{t: t, shop: shop, em: em}
}

func (e *env) ask(typ string, payload any) []byte {
	e.t.Helper()
	r := e.em.Ask(typ, payload)
	if r.Err != nil {
		e.t.Fatalf("%s: %v", typ, r.Err)
	}
	return r.Answer
}

func (e *env) submitted(kind string) []json.RawMessage {
	var out []json.RawMessage
	for _, b := range e.em.Host.Submitted {
		if b.Kind == kind {
			out = append(out, b.Records...)
		}
	}
	return out
}

func TestConnectTestReportsTheShopAndWritesNothing(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	before := e.shop.Fingerprint()
	var got channel.ConnectTestResult
	if err := json.Unmarshal(e.ask(channel.AskConnectTest, struct{}{}), &got); err != nil {
		t.Fatal(err)
	}
	if got.AccountName != "Demo Shop A" || got.Products != 50 || got.Orders != 3 || len(got.Locations) != 1 || len(got.Scopes) != 3 {
		t.Fatalf("result %+v", got)
	}
	if e.shop.Writes() != 0 || e.shop.Fingerprint() != before || len(e.em.Host.Submitted) != 0 || len(e.em.Host.CursorKeys()) != 0 {
		t.Fatal("connect test changed something")
	}
}

func TestOAuthCallbackReturnsTheVerifiedAccountAndScopes(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	var got channel.OAuthCallbackResult
	in := channel.OAuthCallbackInput{Params: map[string]string{"shop": "shop-a.demo-shop.example", "scope": "products,inventory,orders", "state": "s"}}
	if err := json.Unmarshal(e.ask(channel.AskOAuthCallback, in), &got); err != nil {
		t.Fatal(err)
	}
	if got.Account != "shop-a.demo-shop.example" || strings.Join(got.Scopes, ",") != "products,inventory,orders" {
		t.Fatalf("result %+v", got)
	}
	if e.em.Host.Calls[channel.FnHTTP] != 0 {
		t.Fatal("the callback ask makes no network call")
	}
	for _, bad := range []string{"evil.example", "shop-a.demo-shop.example.evil.example", "demo-shop.example", "a/b.demo-shop.example", "Shop-A.demo-shop.example", ""} {
		in.Params["shop"] = bad
		if r := e.em.Ask(channel.AskOAuthCallback, in); r.Err == nil {
			t.Errorf("account %q accepted", bad)
		}
	}
}

func TestWebhookIsParsedWithoutTenantContext(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	cases := []struct{ topic, event string }{
		{"orders/create", channel.EventOrderChanged}, {"orders/updated", channel.EventOrderChanged},
		{"app/uninstalled", channel.EventUninstalled}, {"customers/redact", channel.EventCustomerRedact},
		{"shop/redact", channel.EventShopRedact}, {"customers/data_request", channel.EventDataRequest},
		{"something/else", channel.EventOther},
	}
	for _, c := range cases {
		body := e.shop.WebhookBody("evt-"+c.topic, c.topic, "o-9001")
		var got channel.WebhookResult
		in := channel.WebhookInput{Headers: map[string]string{fakeshop.WebhookHeader: e.shop.SignWebhook(body)}, Body: body}
		if err := json.Unmarshal(e.ask(channel.AskWebhook, in), &got); err != nil {
			t.Fatal(err)
		}
		if got.Event != c.event || got.Account != "shop-a.demo-shop.example" || got.DedupeID != "evt-"+c.topic || len(got.ResourceIDs) != 1 || got.ResourceIDs[0] != "o-9001" {
			t.Errorf("%s: %+v", c.topic, got)
		}
	}
	for _, fn := range []string{channel.FnHTTP, channel.FnCursorGet, channel.FnCursorSet, channel.FnSubmit, channel.FnOutboxNext} {
		if e.em.Host.Calls[fn] != 0 {
			t.Errorf("the webhook ask called %s: it has no tenant context", fn)
		}
	}
	if r := e.em.Ask(channel.AskWebhook, channel.WebhookInput{Body: []byte("not json")}); r.Err == nil {
		t.Error("a body that is not a webhook was accepted")
	}
}

func TestImportSubmitsEveryProductWithVariantsAndStock(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	var ans struct {
		Done     bool  `json:"done"`
		Imported int64 `json:"imported"`
	}
	if err := json.Unmarshal(e.ask(channel.AskImport, struct{}{}), &ans); err != nil {
		t.Fatal(err)
	}
	if !ans.Done || ans.Imported != 50 {
		t.Fatalf("answer %+v", ans)
	}
	listings := e.submitted(channel.KindListing)
	if len(listings) != 50 {
		t.Fatalf("%d listings", len(listings))
	}
	for _, b := range e.em.Host.Submitted {
		if len(b.Records) > channel.MaxBatchRecords {
			t.Fatalf("batch of %d", len(b.Records))
		}
	}
	var first channel.Listing
	if err := json.Unmarshal(listings[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.ExternalID != "p-0001" || first.ExternalVersion == "" || len(first.Variants) != 2 || first.Variants[0].SKU != "DEMO-0001-S" ||
		first.Variants[0].PriceMinor != 510 || first.Variants[0].Currency != "GBP" || first.Variants[0].CompareAtMinor != 710 || !first.Variants[0].Tracked {
		t.Fatalf("listing %+v", first)
	}
	if n := len(e.submitted(channel.KindStockObserved)); n < 50 {
		t.Fatalf("%d stock observations", n)
	}
	if _, err := e.em.Host.Cursors["import.products"], error(nil); err != nil || len(e.em.Host.Cursors["import.products"]) != 0 {
		t.Fatal("a finished import leaves no resume cursor")
	}
	if e.shop.Writes() != 0 {
		t.Fatal("import wrote to the channel")
	}
}

func TestImportTwiceSubmitsTheSameRecords(t *testing.T) {
	// "A second run gives zero changes" is the host's call, on
	// (connection, kind, external_id, external_version): the adapter must
	// therefore send byte-identical records for unchanged products.
	e := newEnv(t, fakeshop.Options{})
	e.ask(channel.AskImport, struct{}{})
	first := e.submitted(channel.KindListing)
	e.em.Host.Submitted = nil
	e.ask(channel.AskImport, struct{}{})
	second := e.submitted(channel.KindListing)
	if len(first) != len(second) {
		t.Fatalf("%d then %d", len(first), len(second))
	}
	for i := range first {
		if string(first[i]) != string(second[i]) {
			t.Fatalf("record %d differs between runs:\n%s\n%s", i, first[i], second[i])
		}
	}
}

func TestImportResumesFromTheCursorAfterAFailure(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	// The channel stops answering after the second page.
	calls := 0
	inner := e.em.Host.HTTP
	e.em.Host.HTTP = func(r channel.HTTPRequest) (channel.HTTPResponse, error) {
		calls++
		if calls > 2 {
			return channel.HTTPResponse{}, channel.ErrInternal
		}
		return inner(r)
	}
	if r := e.em.Ask(channel.AskImport, struct{}{}); r.Err == nil {
		t.Fatal("a failed page must fail the ask")
	}
	got := len(e.submitted(channel.KindListing))
	if got != 40 || string(e.em.Host.Cursors["import.products"]) != "40" {
		t.Fatalf("after the failure: %d listings, cursor %q", got, e.em.Host.Cursors["import.products"])
	}
	e.em.Host.HTTP = inner
	e.ask(channel.AskImport, struct{}{})
	if n := len(e.submitted(channel.KindListing)); n != 50 {
		t.Fatalf("%d listings in total: the resume must continue, not restart", n)
	}
	if len(e.em.Host.Cursors["import.products"]) != 0 {
		t.Fatal("cursor left after the import finished")
	}
}

func TestOrdersAreSubmittedWithoutPersonalData(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	e.ask(channel.AskFetch, channel.FetchInput{Event: channel.EventOrderChanged, ResourceIDs: []string{"o-9001", "o-9002"}})
	orders := e.submitted(channel.KindOrder)
	if len(orders) != 2 {
		t.Fatalf("%d orders", len(orders))
	}
	for _, o := range orders {
		for _, secret := range []string{"Invented Person", "invented@example.invalid", "+440000000000", "1 Invented Street"} {
			if strings.Contains(string(o), secret) {
				t.Fatalf("order carries %q: %s", secret, o)
			}
		}
	}
	var o map[string]any
	if err := json.Unmarshal(orders[0], &o); err != nil {
		t.Fatal(err)
	}
	// Required fields of a submitted order (contract section 2).
	for _, k := range []string{"external_id", "external_version", "order_number", "placed_at", "currency", "status", "lines",
		"shipping_minor", "shipping_tax_bp", "shipping_tax_included", "order_discount_minor", "tax_added_minor", "refund_total_minor", "total_minor"} {
		if _, ok := o[k]; !ok {
			t.Errorf("order lacks %q", k)
		}
	}
	line := o["lines"].([]any)[0].(map[string]any)
	for _, k := range []string{"external_id", "sku", "name", "qty", "unit_price_minor", "tax_bp", "tax_included", "discount_line_minor", "refunded_quantity", "restocked_quantity"} {
		if _, ok := line[k]; !ok {
			t.Errorf("line lacks %q", k)
		}
	}
	if o["status"] != "new" || o["order_number"] != "#1001" || o["customer"].(map[string]any)["external_id"] != "c-71" {
		t.Fatalf("order %v", o)
	}
	if e.shop.Writes() != 0 {
		t.Fatal("fetch wrote to the channel")
	}
}

func TestTickPullsOrdersUpdatedSinceTheCursor(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	e.ask(channel.AskTick, map[string]string{"event": "reconcile"})
	if n := len(e.submitted(channel.KindOrder)); n != 3 {
		t.Fatalf("%d orders on the first tick", n)
	}
	if len(e.em.Host.Cursors["orders.since"]) == 0 {
		t.Fatal("no cursor")
	}
	e.em.Host.Submitted = nil
	e.shop.FulfilOutsideUT("o-9001")
	e.ask(channel.AskTick, map[string]string{"event": "reconcile"})
	orders := e.submitted(channel.KindOrder)
	var shipped bool
	for _, raw := range orders {
		var o struct {
			ExternalID string `json:"external_id"`
			Status     string `json:"status"`
		}
		_ = json.Unmarshal(raw, &o)
		if o.ExternalID == "o-9001" && o.Status == "shipped" {
			shipped = true
		}
		if o.ExternalID == "o-9002" {
			t.Fatal("an order older than the cursor was pulled again")
		}
	}
	if !shipped {
		t.Fatalf("the changed order was not pulled: %d orders", len(orders))
	}
}

func TestContactIsFetchedLiveAndNeverSubmitted(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	var got channel.ContactResult
	if err := json.Unmarshal(e.ask(channel.AskContact, channel.ContactInput{OrderExternalID: "o-9001"}), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Invented Person" || got.Email == "" {
		t.Fatalf("contact %+v", got)
	}
	if len(e.em.Host.Submitted) != 0 || len(e.em.Host.CursorKeys()) != 0 {
		t.Fatal("contact details were stored")
	}
	for _, l := range e.em.Host.Logs {
		if b, _ := json.Marshal(l); strings.Contains(string(b), "Invented") {
			t.Fatal("contact details were logged")
		}
	}
}

func op(id, kind, ext string, expect *int64, payload string) channel.OutboxOp {
	return channel.OutboxOp{ID: id, Kind: kind, ExternalID: ext, Expect: expect, Payload: json.RawMessage(payload)}
}

func TestStockSetIsAbsoluteAndAStaleExpectReReads(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	v := "v-0001-1"
	start := e.shop.Stock(v)
	e.em.Host.Outbox = []channel.OutboxOp{op("op-1", channel.OpStockSet, v, &start, `{"available":7}`)}
	e.ask(channel.AskOutbox, struct{}{})
	if e.shop.Stock(v) != 7 || e.em.Host.Acks["op-1"].Error != nil {
		t.Fatalf("stock %d ack %+v", e.shop.Stock(v), e.em.Host.Acks["op-1"])
	}
	// Setting the same absolute value again is harmless (idempotent).
	seven := int64(7)
	e.em.Host.Outbox = []channel.OutboxOp{op("op-2", channel.OpStockSet, v, &seven, `{"available":7}`)}
	e.ask(channel.AskOutbox, struct{}{})
	if e.shop.Stock(v) != 7 {
		t.Fatal("not absolute")
	}
	// The merchant sold two in the channel's own admin: the expect is stale.
	e.shop.SetStock(v, 5)
	e.em.Host.Submitted = nil
	e.em.Host.Outbox = []channel.OutboxOp{op("op-3", channel.OpStockSet, v, &seven, `{"available":9}`)}
	e.ask(channel.AskOutbox, struct{}{})
	if e.shop.Stock(v) != 5 {
		t.Fatalf("a stale expect must not write; stock %d", e.shop.Stock(v))
	}
	ack := e.em.Host.Acks["op-3"]
	if ack.Error == nil || ack.Error.Code != "stale_expect" || !ack.Error.Retryable {
		t.Fatalf("ack %+v", ack)
	}
	obs := e.submitted(channel.KindStockObserved)
	if len(obs) != 1 || !strings.Contains(string(obs[0]), `"available":5`) {
		t.Fatalf("the channel's current value was not reported: %s", obs)
	}
	if e.submitted(channel.KindOrder) == nil {
		t.Fatal("a stale CAS pulls recent orders before the cloud recomputes (ADR-0169 section 8)")
	}
}

func TestFulfilmentAndCancelReachTheChannelOnceAndAreIdempotentOnRetry(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	ful := op("op-f", channel.OpFulfilmentCreate, "o-9001", nil, `{"external_order_id":"o-9001","carrier":"Invented Post","tracking_number":"TRK-1"}`)
	e.em.Host.Outbox = []channel.OutboxOp{ful}
	e.ask(channel.AskOutbox, struct{}{})
	if e.shop.Performed("fulfil", "o-9001") != 1 || e.em.Host.Acks["op-f"].Error != nil {
		t.Fatalf("ack %+v", e.em.Host.Acks["op-f"])
	}
	// The same operation delivered again (the ack was lost): same
	// Idempotency-Key, no second effect, still success.
	e.em.Host.Outbox = []channel.OutboxOp{ful}
	e.ask(channel.AskOutbox, struct{}{})
	if e.shop.Performed("fulfil", "o-9001") != 1 || e.em.Host.Acks["op-f"].Error != nil {
		t.Fatal("a retried fulfilment acted twice or failed")
	}
	// A different operation for an order the shop already fulfilled in the
	// channel's own admin: acknowledged as success, not dead-lettered.
	e.shop.FulfilOutsideUT("o-9002")
	e.em.Host.Outbox = []channel.OutboxOp{op("op-f2", channel.OpFulfilmentCreate, "o-9002", nil, `{"external_order_id":"o-9002","carrier":"Invented Post"}`)}
	e.ask(channel.AskOutbox, struct{}{})
	if e.em.Host.Acks["op-f2"].Error != nil {
		t.Fatalf("already-fulfilled must be success: %+v", e.em.Host.Acks["op-f2"])
	}
	// Cancel, the same two ways.
	can := op("op-c", channel.OpOrderCancel, "o-9003", nil, `{"external_order_id":"o-9003","reason":"customer_request","restock":true}`)
	e.em.Host.Outbox = []channel.OutboxOp{can}
	e.ask(channel.AskOutbox, struct{}{})
	e.em.Host.Outbox = []channel.OutboxOp{can}
	e.ask(channel.AskOutbox, struct{}{})
	if e.shop.Performed("cancel", "o-9003") != 1 || e.em.Host.Acks["op-c"].Error != nil {
		t.Fatal("cancel acted twice or failed on retry")
	}
}

func TestOutboxRefusesWhatItCannotDoAndNeverDeletes(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	e.em.Host.Outbox = []channel.OutboxOp{
		op("op-u", "listing.delete", "p-0001", nil, `{}`),
		op("op-x", channel.OpOrderCancel, "o-9001", nil, `{"external_order_id":"o-9001","reason":"not a reason","restock":false}`),
		op("op-p", channel.OpFulfilmentCreate, "../../admin", nil, `{"external_order_id":"../../admin","carrier":"c"}`),
	}
	e.ask(channel.AskOutbox, struct{}{})
	for _, id := range []string{"op-u", "op-x", "op-p"} {
		if a := e.em.Host.Acks[id]; a.Error == nil || a.Error.Retryable {
			t.Errorf("%s: want a non-retryable error, got %+v", id, a)
		}
	}
	for _, r := range e.em.Seen() {
		if r.Method == "DELETE" {
			t.Fatal("the adapter tried to delete")
		}
		if strings.Contains(r.URL, "admin") {
			t.Errorf("an id with path characters reached a URL: %s", r.URL)
		}
	}
}

func TestHostFunctionDenialIsAnErrorNotASilentSuccess(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	e.em.Host.Deny[channel.FnHTTP] = true
	if r := e.em.Ask(channel.AskConnectTest, struct{}{}); r.Err == nil || !errors.Is(r.Err, channel.ErrDenied) {
		t.Fatalf("want ErrDenied, got %v", r.Err)
	}
}
