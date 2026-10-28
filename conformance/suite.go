//go:build !wasip1

package conformance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/sdk/plugin/channel"
)

// Hostile modes a channel's fake server must be able to switch into. They
// model a malicious or broken CHANNEL (the adapter is trusted code that
// could still be tricked by its server).
const (
	EndlessPages = "endless_pages" // list calls never end
	ForeignLinks = "foreign_links" // absolute links to another host, ids with path tricks
	EchoHeaders  = "echo_headers"  // data echoes every request header, credential included
	HugeBody     = "huge_body"     // a list answer over the 8 MiB response cap
)

// Shop is the adapter's own fake channel server, as the suite needs it.
type Shop interface {
	http.Handler
	// Writes counts requests that were not GET or HEAD.
	Writes() int
	// Fingerprint changes whenever anything the shop holds changes.
	Fingerprint() string
	// Hostile switches the shop into one of the modes above.
	Hostile(mode string)
}

// Target is an adapter under test plus its invented shop.
type Target struct {
	Handlers channel.Handlers
	// NewShop returns a fresh, seeded shop (no hostile mode).
	NewShop func() Shop
	// Account is the connection's account host; CredentialHeader the
	// manifest's auth.inject.header; Credential the token the shop accepts.
	Account, CredentialHeader, Credential string
	// StockVariant, when set, is a tracked variant id NewShop seeds; check 10
	// sets its stock at every location the connect test lists. Without it
	// check 10 skips.
	StockVariant string
}

// rig is one fresh connection. t installs the SDK's fake host for its life.
func (tg Target) rig(t TB) (Shop, *Emulator) {
	t.Helper()
	shop := tg.NewShop()
	return shop, NewEmulator(t, tg.Handlers, shop, tg.Account, tg.CredentialHeader, tg.Credential)
}

// Run is the whole suite: ten checks: nine in the order of the
// contract's section 6, then check 10 (section 4.1: the connect test's
// locations). A check whose dependency has not been built yet is a
// clearly-marked TODO that skips with the card it waits for; it never passes
// silently.
func Run(t *testing.T, tg Target) {
	report := func(t *testing.T, violations []string) {
		t.Helper()
		for _, v := range violations {
			t.Error(v)
		}
	}
	t.Run("1_connect_test_reads_and_writes_nothing", func(t *testing.T) { report(t, Check1ConnectReadsAndWritesNothing(t, tg)) })
	t.Run("2_import_50_products_second_run_zero_changes", func(t *testing.T) {
		t.Skip("TODO ut-docs#4765 (catalogue sync): needs the cloud's linking and its unchanged/accepted accounting. " +
			"Adapter side (deterministic, byte-identical records on a second run) is pinned in src/app TestImportTwiceSubmitsTheSameRecords.")
	})
	t.Run("3_webhook_verification", func(t *testing.T) {
		t.Skip("TODO ut-docs#4762 (webhook ingress): valid, bad signature, wrong account, unknown connection, oversized body (identical 401) and the signed duplicate (200, no second effect) are the HOST's route. " +
			"Adapter side (parse only, no tenant context) is pinned in src/app TestWebhookIsParsedWithoutTenantContext; the fake shop signs bodies with SignWebhook for the host-side test.")
	})
	t.Run("4_order_creates_exactly_one_channelorder", func(t *testing.T) {
		t.Skip("TODO ut-docs#4767 (order intake): one ChannelOrder after a duplicate and an out-of-order delivery is the cloud's store. " +
			"Adapter side (contract order record, no personal data) is pinned in src/app TestOrdersAreSubmittedWithoutPersonalData.")
	})
	t.Run("5_stock_set_absolute_stale_expect_rereads", func(t *testing.T) {
		t.Skip("TODO ut-docs#4771 (write-back): needs the cloud outbox to issue stock.set with expect. " +
			"Adapter side is pinned in src/app TestStockSetIsAbsoluteAndAStaleExpectReReads.")
	})
	t.Run("6_fulfilment_cancel_refund_exactly_once", func(t *testing.T) {
		t.Skip("TODO ut-docs#4771 (write-back): exactly-once under the host's retry needs the host's outbox attempts. " +
			"Adapter side (Idempotency-Key, already-done is success) is pinned in src/app TestFulfilmentAndCancelReachTheChannelOnceAndAreIdempotentOnRetry; refund.create is not implemented by the demo yet.")
	})
	t.Run("7_no_outbox_operation_deletes", func(t *testing.T) { report(t, Check7NoOperationDeletes(t, tg)) })
	t.Run("8_order_edit_with_restock_moves_stock_exactly", func(t *testing.T) {
		t.Skip("TODO ut-docs#4767 (order intake) and the till's channel-orders card: stock moves are computed from stored revisions by the cloud and the till, not by the adapter. " +
			"The adapter's part (qty never lowered, refunded and restocked quantities passed through) needs the demo shop to support order edits and refunds.")
	})
	t.Run("9_hostile_channel_and_limits", func(t *testing.T) { report(t, Check9HostileChannelAndLimits(t, tg)) })
	t.Run("10_connect_locations_are_stock_set_locations", func(t *testing.T) {
		if tg.StockVariant == "" {
			t.Skip("TODO: this target sets no StockVariant, so the connect test's location_list is not checked against stock.set (contract section 4.1).")
		}
		report(t, Check10ConnectLocationsAreStockSetLocations(t, tg))
	})
}

type violations []string

func (v *violations) addf(format string, args ...any) { *v = append(*v, fmt.Sprintf(format, args...)) }

var allowedMethods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true}

// Check1ConnectReadsAndWritesNothing: the connect test reports the account
// and writes nothing anywhere: no request but GET or HEAD, the shop is
// byte-for-byte unchanged, nothing submitted, no cursor, no outbox, no ack.
// It returns what it found wrong; empty means the adapter conforms.
func Check1ConnectReadsAndWritesNothing(t TB, tg Target) []string {
	t.Helper()
	var v violations
	shop, em := tg.rig(t)
	before := shop.Fingerprint()
	r := em.Ask(channel.AskConnectTest, struct{}{})
	if r.Err != nil {
		v.addf("connect test failed: %v", r.Err)
		return v
	}
	var ans channel.ConnectTestResult
	if err := json.Unmarshal(r.Answer, &ans); err != nil || ans.AccountName == "" {
		v.addf("connect test must report the account name; got %q (%v)", r.Answer, err)
	}
	if len(em.Seen()) == 0 {
		v.addf("connect test made no request: it must really read the account")
	}
	for _, s := range em.Seen() {
		if s.Method != "GET" && s.Method != "HEAD" && s.Method != "" {
			v.addf("connect test sent %s %s", s.Method, s.URL)
		}
	}
	if shop.Writes() != 0 {
		v.addf("the shop saw %d write request(s)", shop.Writes())
	}
	if shop.Fingerprint() != before {
		v.addf("the shop changed during the connect test")
	}
	h := em.Host
	if len(h.Submitted) != 0 {
		v.addf("connect test submitted %d batch(es)", len(h.Submitted))
	}
	if len(h.CursorKeys()) != 0 || h.Calls[channel.FnCursorSet] != 0 {
		v.addf("connect test wrote a cursor")
	}
	if h.Calls[channel.FnOutboxNext] != 0 || h.Calls[channel.FnOutboxAck] != 0 || len(h.Acks) != 0 {
		v.addf("connect test touched the outbox")
	}
	return v
}

// opKinds are all outbound operation kinds of the contract (section 3).
var opKinds = []string{
	channel.OpListingUpsert, channel.OpListingUnpublish, channel.OpStockSet, channel.OpFulfilmentCreate,
	channel.OpOrderCancel, channel.OpRefundCreate, channel.OpImageUpsert,
}

// Check7NoOperationDeletes: whatever the cloud queues, the adapter never
// sends a DELETE (the contract has no delete anywhere); a deactivated item
// is listing.unpublish, a draft or unlisted product at the channel.
func Check7NoOperationDeletes(t TB, tg Target) []string {
	t.Helper()
	var v violations
	_, em := tg.rig(t)
	for i, k := range opKinds {
		em.Host.Outbox = append(em.Host.Outbox, channel.OutboxOp{
			ID: fmt.Sprintf("op-%d", i), Kind: k, ExternalID: "o-9001",
			Payload: json.RawMessage(`{"external_order_id":"o-9001","available":3,"carrier":"c","reason":"other","restock":false}`),
		})
	}
	em.Host.Outbox = append(em.Host.Outbox, channel.OutboxOp{ID: "op-del", Kind: "listing.delete", ExternalID: "p-0001"})
	r := em.Ask(channel.AskOutbox, struct{}{})
	if r.Err != nil {
		v.addf("outbox ask: %v", r.Err)
		return v
	}
	for _, s := range em.Seen() {
		if strings.EqualFold(s.Method, "DELETE") {
			v.addf("the adapter sent DELETE %s", s.URL)
		}
	}
	if a, ok := em.Host.Acks["op-del"]; ok && a.Error == nil {
		v.addf("an unknown delete operation was acknowledged as success")
	}
	return v
}

// hostileIDs are ids a hostile channel could hand the adapter, or a
// confused cloud could queue: they must never reach a URL as path, query or
// another host.
var hostileIDs = []string{"../../admin", "o-9001?x=1", "o-9001#frag", "o-9001/../../x", "https://evil.example/o-9001", strings.Repeat("a", 5000), "a b"}

// drive gives the adapter every ask it may be given, hostile ids where ids go.
func drive(em *Emulator) {
	em.Ask(channel.AskConnectTest, struct{}{})
	em.Ask(channel.AskImport, struct{}{})
	em.Ask(channel.AskTick, map[string]string{"event": "reconcile"})
	em.Ask(channel.AskFetch, channel.FetchInput{Event: channel.EventOrderChanged, ResourceIDs: hostileIDs})
	em.Ask(channel.AskContact, channel.ContactInput{OrderExternalID: "../../admin"})
	for i, id := range hostileIDs {
		for _, k := range []string{channel.OpStockSet, channel.OpFulfilmentCreate, channel.OpOrderCancel} {
			em.Host.Outbox = append(em.Host.Outbox, channel.OutboxOp{
				ID: fmt.Sprintf("h-%s-%d", k, i), Kind: k, ExternalID: id,
				Payload: json.RawMessage(fmt.Sprintf(`{"external_order_id":%q,"available":1,"carrier":"c","reason":"other"}`, id)),
			})
		}
	}
	em.Ask(channel.AskOutbox, struct{}{})
}

// Check9HostileChannelAndLimits: a hostile channel cannot make the adapter
// leave {account}, cannot make it leak the credential, and cannot push it
// past the host's caps or memory. (The sandbox's own enforcement of the same
// limits on a hostile PLUGIN is ut-cloud internal/channelhost TestEgress_*
// and TestHostile_*; this checks the adapter stays inside them by itself.)
func Check9HostileChannelAndLimits(t TB, tg Target) []string {
	t.Helper()
	var v violations

	// -- egress stays on the account, with and without a hostile channel.
	for _, mode := range []string{"", ForeignLinks} {
		shop, em := tg.rig(t)
		if mode != "" {
			shop.Hostile(mode)
		}
		drive(em)
		if len(em.Seen()) == 0 {
			v.addf("[%s] the adapter made no request at all", mode)
		}
		for _, s := range em.Seen() {
			u, err := url.Parse(s.URL)
			switch {
			case err != nil || u.Scheme != "https" || u.Hostname() != tg.Account || u.User != nil || (u.Port() != "" && u.Port() != "443"):
				v.addf("[%s] request left {account}: %s %.120s", mode, s.Method, s.URL)
				continue
			case !allowedMethods[strings.ToUpper(s.Method)] && s.Method != "":
				v.addf("[%s] method %s is not allowed", mode, s.Method)
			case s.Denied:
				v.addf("[%s] the host had to deny %s %.120s: the adapter must not attempt it", mode, s.Method, s.URL)
			}
			for _, seg := range strings.Split(u.EscapedPath(), "/") {
				if seg == ".." || seg == "." {
					v.addf("[%s] a path segment %q reached a URL: %.120s", mode, seg, s.URL)
				}
			}
			if strings.Contains(u.RawQuery, "x=1") || u.Fragment != "" || strings.Contains(s.URL, "#") {
				v.addf("[%s] an id turned into a query or fragment: %.120s", mode, s.URL)
			}
			for k := range s.Header {
				if strings.EqualFold(k, tg.CredentialHeader) {
					v.addf("[%s] the adapter set the credential header itself", mode)
				}
			}
		}
	}

	// -- the credential never reaches anything the adapter produces.
	{
		shop, em := tg.rig(t)
		shop.Hostile(EchoHeaders)
		drive(em)
		all := []string{string(em.Ask(channel.AskConnectTest, struct{}{}).Answer)}
		for _, b := range em.Host.Submitted {
			for _, r := range b.Records {
				all = append(all, string(r))
			}
		}
		for k, val := range em.Host.Cursors {
			all = append(all, k, string(val))
		}
		for _, l := range em.Host.Logs {
			b, _ := json.Marshal(l)
			all = append(all, string(b))
		}
		for id, a := range em.Host.Acks {
			b, _ := json.Marshal(a)
			all = append(all, id, string(b))
		}
		for _, s := range all {
			if strings.Contains(s, tg.Credential) {
				v.addf("the credential reached adapter output: %.200s", s)
				break
			}
		}
	}

	// -- endless pages stop inside the caps, in memory and in time.
	{
		shop, em := tg.rig(t)
		shop.Hostile(EndlessPages)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for i := 0; i < 3; i++ { // the cursor lets the next ask continue
			r := em.Ask(channel.AskImport, struct{}{})
			if r.Err != nil {
				v.addf("endless pages: ask %d failed: %v", i, r.Err)
				break
			}
			if r.Elapsed > time.Duration(channel.ImportCallSecs)*time.Second {
				v.addf("endless pages: ask %d took %s, over the deadline", i, r.Elapsed)
			}
			if c := em.HTTPCalls(); c > MaxHTTPCallsPerAsk {
				v.addf("endless pages: ask %d made %d channel_http calls (cap %d)", i, c, MaxHTTPCallsPerAsk)
			}
		}
		n := 0
		for _, b := range em.Host.Submitted {
			n += len(b.Records)
		}
		if n > 3*MaxRecordsPerAsk {
			v.addf("endless pages: %d records submitted by three asks (cap %d each)", n, MaxRecordsPerAsk)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		if grew := int64(after.HeapAlloc) - int64(before.HeapAlloc); grew > channel.MaxMemoryBytes {
			v.addf("endless pages: heap grew by %d bytes (guest memory cap %d)", grew, channel.MaxMemoryBytes)
		}
	}

	// -- an answer over the response cap is an error, not a hang or a partial import.
	{
		shop, em := tg.rig(t)
		shop.Hostile(HugeBody)
		r := em.Ask(channel.AskImport, struct{}{})
		if r.Err == nil {
			v.addf("an answer over the response cap must fail the ask (the host answers -5)")
		}
		if len(em.Host.Submitted) != 0 {
			v.addf("records were submitted from an answer the host refused")
		}
	}
	return v
}

// Check10ConnectLocationsAreStockSetLocations: the ids the connect test lists
// in location_list (contract section 4.1) are ids stock.set accepts as
// payload.location (section 8.3), and an id it does not list is refused. The
// connect test is what the merchant picks locations from; an id the write-back
// cannot use would fail every stock push for good. Needs Target.StockVariant.
func Check10ConnectLocationsAreStockSetLocations(t TB, tg Target) []string {
	t.Helper()
	var v violations
	_, em := tg.rig(t)
	r := em.Ask(channel.AskConnectTest, struct{}{})
	if r.Err != nil {
		v.addf("connect test failed: %v", r.Err)
		return v
	}
	var ans struct {
		LocationList []struct {
			ID     string `json:"id"`
			Active *bool  `json:"active"` // absent means active
		} `json:"location_list"`
	}
	if err := json.Unmarshal(r.Answer, &ans); err != nil {
		v.addf("connect test answer is not valid JSON: %v", err)
		return v
	}
	if len(ans.LocationList) == 0 {
		v.addf("connect test sends no location_list: the target sets StockVariant, so its channel has stock locations to list")
		return v
	}
	// stockSetAt asks a FRESH connection to set the variant's stock at loc.
	stockSetAt := func(opID, loc string) (ack channel.Ack, acked, changed bool) {
		shop, em := tg.rig(t)
		before := shop.Fingerprint()
		em.Host.Outbox = append(em.Host.Outbox, channel.OutboxOp{
			ID: opID, Kind: channel.OpStockSet, ExternalID: tg.StockVariant + "@" + loc,
			Payload: json.RawMessage(fmt.Sprintf(`{"available":777,"location":%q}`, loc)),
		})
		if r := em.Ask(channel.AskOutbox, struct{}{}); r.Err != nil {
			v.addf("outbox ask for location %q: %v", loc, r.Err)
		}
		ack, acked = em.Host.Acks[opID]
		return ack, acked, shop.Fingerprint() != before
	}
	first := ""
	for i, l := range ans.LocationList {
		if l.Active != nil && !*l.Active {
			continue
		}
		if first == "" {
			first = l.ID
		}
		ack, acked, changed := stockSetAt(fmt.Sprintf("loc-%d", i), l.ID)
		switch {
		case !acked:
			v.addf("stock.set at the listed location %q was not acknowledged", l.ID)
		case ack.Error != nil:
			v.addf("stock.set refused the location %q that the connect test lists (%s)", l.ID, ack.Error.Code)
		case !changed:
			v.addf("stock.set at the listed location %q was acknowledged without changing the channel", l.ID)
		}
	}
	if first == "" {
		v.addf("connect test lists no active location")
		return v
	}
	unlisted := first + "9"
	for _, l := range ans.LocationList {
		if l.ID == unlisted {
			unlisted += "9"
		}
	}
	ack, acked, changed := stockSetAt("loc-unlisted", unlisted)
	if acked && ack.Error == nil {
		v.addf("stock.set accepted location %q that the connect test does not list: the adapter ignores payload.location", unlisted)
	}
	if changed {
		v.addf("stock.set at the unlisted location %q changed the channel", unlisted)
	}
	return v
}
