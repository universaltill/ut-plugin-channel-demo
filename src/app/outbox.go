package app

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/sdk/plugin/channel"
)

const maxOutboxRounds = 20

var cancelReasons = map[string]bool{"customer_request": true, "out_of_stock": true, "fraud": true, "other": true}

// drainOutbox performs the pending outbound operations one at a time and
// acknowledges each. The adapter has no delete: an operation it cannot or
// will not do is dead-lettered with a non-retryable error. Every write that
// is not naturally idempotent carries the operation id as its
// Idempotency-Key, so a retry after a lost answer acts once.
func drainOutbox(channel.Event) (any, error) {
	done := 0
	for round := 0; round < maxOutboxRounds; round++ {
		ops, err := channel.OutboxNext(channel.MaxOutboxBatch)
		if err != nil {
			return nil, err
		}
		if len(ops) == 0 {
			break
		}
		acked := 0
		for _, op := range ops {
			ack := perform(op)
			if err := channel.OutboxAck(op.ID, ack); err != nil {
				logErr("outbox.ack_failed", err)
				continue
			}
			acked++
			done++
		}
		if acked == 0 {
			break
		}
	}
	return map[string]any{"done": done}, nil
}

func fail(code string, retryable bool) channel.Ack {
	return channel.Ack{Error: &channel.AckError{Code: code, Retryable: retryable}}
}

// failFor classifies a transport or status error. A 4xx other than the cases
// handled by the caller can never succeed on retry; a 5xx or a lost host
// call can.
func failFor(err error) channel.Ack {
	var se *statusError
	switch {
	case errors.As(err, &se):
		if se.Status >= 500 || se.Status == 408 {
			return fail("channel_error", true)
		}
		return fail("rejected_by_channel", false)
	case errors.Is(err, channel.ErrDenied):
		return fail("egress_denied", false)
	case errors.Is(err, channel.ErrQuota):
		return fail("rate_limited", true)
	}
	return fail("channel_unreachable", true)
}

func perform(op channel.OutboxOp) channel.Ack {
	switch op.Kind {
	case channel.OpStockSet:
		return stockSet(op)
	case channel.OpFulfilmentCreate:
		return fulfil(op)
	case channel.OpOrderCancel:
		return cancel(op)
	}
	return fail("unsupported_op", false)
}

// stockLocation splits a stock.set external id, `<variant>` or
// `<variant>@<location>` (contract section 8.3), and reconciles it with the
// payload's optional location. Both parts pass okID; two locations that differ
// are refused. The location is "" when the operation names none.
func stockLocation(externalID, payloadLoc string) (variant, loc string, ok bool) {
	variant, idLoc, hasLoc := strings.Cut(externalID, "@")
	if !okID(variant) || (hasLoc && !okID(idLoc)) || (payloadLoc != "" && !okID(payloadLoc)) {
		return "", "", false
	}
	if hasLoc && payloadLoc != "" && idLoc != payloadLoc {
		return "", "", false
	}
	if hasLoc {
		return variant, idLoc, true
	}
	return variant, payloadLoc, true
}

func stockSet(op channel.OutboxOp) channel.Ack {
	var p struct {
		Available *int64  `json:"available"`
		Location  *string `json:"location"`
	}
	if json.Unmarshal(op.Payload, &p) != nil || p.Available == nil || *p.Available < 0 {
		return fail("invalid_payload", false)
	}
	payloadLoc := ""
	if p.Location != nil {
		if *p.Location == "" {
			return fail("invalid_payload", false)
		}
		payloadLoc = *p.Location
	}
	variant, loc, ok := stockLocation(op.ExternalID, payloadLoc)
	if !ok {
		return fail("invalid_payload", false)
	}
	body := map[string]any{"available": *p.Available}
	if op.Expect != nil {
		body["expect"] = *op.Expect
	}
	if loc != "" {
		body["location"] = loc
	}
	st, raw, err := call("PUT", "/api/inventory/"+esc(variant), map[string]string{"Idempotency-Key": op.ID}, body)
	if err != nil {
		return failFor(err)
	}
	switch {
	case st == 200:
		return channel.Ack{ExternalID: op.ExternalID, ExternalVersion: strconv.FormatInt(*p.Available, 10)}
	case st == 409 && errCode(raw) == "stale":
		return staleStock(op, variant, raw)
	case st == 404:
		// Also the shop's unknown_location: nothing was written.
		return fail("not_found", false)
	}
	return failFor(&statusError{Status: st})
}

// staleStock handles a compare-and-set that lost: the channel's value is not
// the one the cloud last saw. Report what the channel holds, pull recent
// orders (ADR-0169 section 8: orders explain the difference), and let the
// cloud recompute and requeue. Nothing was written.
func staleStock(op channel.OutboxOp, variant string, raw []byte) channel.Ack {
	var cur struct {
		Available *int64 `json:"available"`
	}
	if json.Unmarshal(raw, &cur) == nil && cur.Available != nil && *cur.Available >= 0 {
		if _, err := submit(channel.KindStockObserved, []channel.StockObserved{observed(variant, *cur.Available, "channel-read")}); err != nil {
			logErr("outbox.stale_report_failed", err)
		}
	}
	if _, err := pullOrders(); err != nil {
		logErr("outbox.stale_orders_failed", err)
	}
	return fail("stale_expect", true)
}

func orderTarget(op channel.OutboxOp, payload string) (string, bool) {
	var p struct {
		ExternalOrderID string `json:"external_order_id"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil {
		return "", false
	}
	id := p.ExternalOrderID
	if id == "" {
		id = op.ExternalID
	}
	if !okID(id) || (op.ExternalID != "" && op.ExternalID != id) {
		return "", false
	}
	return id, true
}

func fulfil(op channel.OutboxOp) channel.Ack {
	id, ok := orderTarget(op, string(op.Payload))
	var p struct {
		Carrier     string `json:"carrier"`
		Tracking    string `json:"tracking_number"`
		TrackingURL string `json:"tracking_url"`
	}
	if !ok || json.Unmarshal(op.Payload, &p) != nil {
		return fail("invalid_payload", false)
	}
	// Values travel as JSON, never interpolated into a path or query.
	body := map[string]any{"carrier": clip(p.Carrier, 100), "tracking_number": clip(p.Tracking, 100), "tracking_url": clip(p.TrackingURL, 500)}
	st, raw, err := call("POST", "/api/orders/"+esc(id)+"/fulfilments", map[string]string{"Idempotency-Key": op.ID}, body)
	if err != nil {
		return failFor(err)
	}
	switch {
	case st == 200, st == 409 && errCode(raw) == "already_fulfilled":
		// Fulfilled by us, by a retry of us, or by the shop in its own
		// admin: all success (a shop that acted there must not dead-letter).
		return channel.Ack{ExternalID: id}
	case st == 409 && errCode(raw) == "order_cancelled":
		return fail("order_cancelled", false)
	case st == 404:
		return fail("not_found", false)
	}
	return failFor(&statusError{Status: st})
}

func cancel(op channel.OutboxOp) channel.Ack {
	id, ok := orderTarget(op, string(op.Payload))
	var p struct {
		Reason  string `json:"reason"`
		Restock bool   `json:"restock"`
	}
	if !ok || json.Unmarshal(op.Payload, &p) != nil || !cancelReasons[p.Reason] {
		return fail("invalid_payload", false)
	}
	st, raw, err := call("POST", "/api/orders/"+esc(id)+"/cancel", map[string]string{"Idempotency-Key": op.ID}, map[string]any{"reason": p.Reason, "restock": p.Restock})
	if err != nil {
		return failFor(err)
	}
	switch {
	case st == 200, st == 409 && errCode(raw) == "already_cancelled":
		return channel.Ack{ExternalID: id}
	case st == 409 && errCode(raw) == "already_fulfilled":
		return fail("already_fulfilled", false)
	case st == 404:
		return fail("not_found", false)
	}
	return failFor(&statusError{Status: st})
}
