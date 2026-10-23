package app

import (
	"errors"
	"net/url"
	"strconv"

	"github.com/universaltill/universal-till/sdk/plugin/channel"
)

const ordersCursor = "orders.since"

type apiOrder struct {
	ID               string `json:"id"`
	Number           string `json:"number"`
	PlacedAt         string `json:"placed_at"`
	UpdatedAt        string `json:"updated_at"`
	Currency         string `json:"currency"`
	State            string `json:"state"`
	Payment          string `json:"payment"`
	ShippingMinor    int64  `json:"shipping_minor"`
	ShippingTaxBP    int64  `json:"shipping_tax_bp"`
	DiscountMinor    int64  `json:"order_discount_minor"`
	TaxAddedMinor    int64  `json:"tax_added_minor"`
	RefundTotalMinor int64  `json:"refund_total_minor"`
	TotalMinor       int64  `json:"total_minor"`
	Lines            []struct {
		ID               string `json:"id"`
		VariantID        string `json:"variant_id"`
		SKU              string `json:"sku"`
		Title            string `json:"title"`
		Quantity         int64  `json:"quantity"`
		UnitPriceMinor   int64  `json:"unit_price_minor"`
		TaxBP            int64  `json:"tax_bp"`
		TaxIncluded      bool   `json:"tax_included"`
		DiscountMinor    int64  `json:"discount_minor"`
		RefundedQuantity int64  `json:"refunded_quantity"`
		RestockedQty     int64  `json:"restocked_quantity"`
	} `json:"lines"`
	// Customer is read for its id and country ONLY. The channel sends name,
	// email, phone and address too; they are not decoded here at all, so
	// they cannot reach a submitted record, a log line or a cursor.
	Customer struct {
		ID      string `json:"id"`
		Country string `json:"country"`
	} `json:"customer"`
}

// orderRecord is the contract's `order` record (sales-channel-v1.md section
// 2). The SDK's channel.Order is thinner than the contract (no order_number,
// tax_included, refund fields, ...), so the adapter owns this type.
type orderRecord struct {
	channel.Identity
	OrderNumber         string               `json:"order_number"`
	PlacedAt            string               `json:"placed_at"`
	Currency            string               `json:"currency"`
	Status              string               `json:"status"`
	PaymentState        string               `json:"payment_state,omitempty"`
	Lines               []orderLineRecord    `json:"lines"`
	ShippingMinor       int64                `json:"shipping_minor"`
	ShippingTaxBP       int64                `json:"shipping_tax_bp"`
	ShippingTaxIncluded bool                 `json:"shipping_tax_included"`
	OrderDiscountMinor  int64                `json:"order_discount_minor"`
	TaxAddedMinor       int64                `json:"tax_added_minor"`
	RefundTotalMinor    int64                `json:"refund_total_minor"`
	TotalMinor          int64                `json:"total_minor"`
	Customer            *channel.CustomerRef `json:"customer,omitempty"`
}

type orderLineRecord struct {
	ExternalID        string `json:"external_id"`
	VariantExternalID string `json:"variant_external_id,omitempty"`
	SKU               string `json:"sku"`
	Name              string `json:"name"`
	Qty               int64  `json:"qty"`
	UnitPriceMinor    int64  `json:"unit_price_minor"`
	TaxBP             int64  `json:"tax_bp"`
	TaxIncluded       bool   `json:"tax_included"`
	DiscountLineMinor int64  `json:"discount_line_minor"`
	RefundedQuantity  int64  `json:"refunded_quantity"`
	RestockedQuantity int64  `json:"restocked_quantity"`
}

// mapOrder turns the channel's order into the contract's record. The status
// is what the channel reports; the cloud owns the canonical one.
func mapOrder(o apiOrder) (orderRecord, bool) {
	if !okID(o.ID) || !timeRe.MatchString(o.UpdatedAt) || !timeRe.MatchString(o.PlacedAt) || len(o.Currency) != 3 {
		return orderRecord{}, false
	}
	r := orderRecord{
		Identity:    channel.Identity{ExternalID: o.ID, ExternalVersion: o.UpdatedAt},
		OrderNumber: clip(o.Number, 40), PlacedAt: o.PlacedAt, Currency: o.Currency, Status: orderStatus(o),
		PaymentState: paymentState(o.Payment), ShippingMinor: o.ShippingMinor, ShippingTaxBP: o.ShippingTaxBP,
		ShippingTaxIncluded: true, OrderDiscountMinor: o.DiscountMinor, TaxAddedMinor: o.TaxAddedMinor,
		RefundTotalMinor: o.RefundTotalMinor, TotalMinor: o.TotalMinor, Lines: []orderLineRecord{},
	}
	for _, l := range o.Lines {
		if !okID(l.ID) {
			return orderRecord{}, false
		}
		lr := orderLineRecord{
			ExternalID: l.ID, SKU: clip(l.SKU, 100), Name: clip(l.Title, 255), Qty: l.Quantity, UnitPriceMinor: l.UnitPriceMinor,
			TaxBP: l.TaxBP, TaxIncluded: l.TaxIncluded, DiscountLineMinor: l.DiscountMinor,
			RefundedQuantity: l.RefundedQuantity, RestockedQuantity: l.RestockedQty,
		}
		if okID(l.VariantID) {
			lr.VariantExternalID = l.VariantID
		}
		r.Lines = append(r.Lines, lr)
	}
	if okID(o.Customer.ID) {
		r.Customer = &channel.CustomerRef{ExternalID: o.Customer.ID, Country: clip(o.Customer.Country, 2)}
	}
	return r, true
}

func orderStatus(o apiOrder) string {
	switch {
	case o.State == "cancelled":
		return "cancelled"
	case o.RefundTotalMinor > 0 && o.RefundTotalMinor >= o.TotalMinor:
		return "refunded"
	case o.State == "fulfilled":
		return "shipped"
	case o.RefundTotalMinor > 0:
		return "partially_refunded"
	}
	return "new"
}

func paymentState(s string) string {
	switch s {
	case "pending", "authorized", "paid", "partially_refunded", "refunded", "voided":
		return s
	}
	return ""
}

// fetch re-fetches the named orders and submits them. A webhook is only a
// doorbell: the order is read from the channel, never taken from the hook.
func fetch(ev channel.Event) (any, error) {
	var in channel.FetchInput
	if err := ev.Decode(&in); err != nil {
		return nil, errors.New("fetch input is not valid JSON")
	}
	if len(in.ResourceIDs) > 100 {
		return nil, errors.New("too many resources")
	}
	var recs []orderRecord
	for _, id := range in.ResourceIDs {
		if !okID(id) {
			channel.Log(channel.LevelWarn, "fetch.skip_id", map[string]any{"reason": "not_an_id"})
			continue
		}
		var o apiOrder
		if err := getJSON("/api/orders/"+esc(id), &o); err != nil {
			var se *statusError
			if errors.As(err, &se) && se.Status == 404 {
				continue // gone in the channel: the host marks the link, nothing is deleted
			}
			return nil, err
		}
		if r, ok := mapOrder(o); ok {
			recs = append(recs, r)
		}
	}
	if len(recs) == 0 {
		return map[string]any{"orders": 0}, nil
	}
	if _, err := submit(channel.KindOrder, recs); err != nil {
		return nil, err
	}
	return map[string]any{"orders": len(recs)}, nil
}

// tick is the scheduled reconciliation: orders updated since the cursor.
// The window is inclusive and the cursor advances only after every page was
// submitted, so a lost run repeats work, never skips it; the host dedupes on
// (connection, kind, external_id, external_version).
func tick(channel.Event) (any, error) {
	n, err := pullOrders()
	if err != nil {
		return nil, err
	}
	return map[string]any{"orders": n}, nil
}

func pullOrders() (int, error) {
	since := cursor(ordersCursor, timeRe)
	newest := since
	total, token := 0, ""
	for pages := 0; pages < maxOrderPages; pages++ {
		var page struct {
			Orders []apiOrder `json:"orders"`
			Next   string     `json:"next_page_token"`
		}
		q := "/api/orders?limit=" + strconv.Itoa(orderPage)
		if since != "" {
			q += "&updated_since=" + url.QueryEscape(since)
		}
		if token != "" {
			q += "&page_token=" + url.QueryEscape(token)
		}
		if err := getJSON(q, &page); err != nil {
			return total, err
		}
		recs := make([]orderRecord, 0, len(page.Orders))
		for _, o := range page.Orders {
			r, ok := mapOrder(o)
			if !ok {
				continue
			}
			recs = append(recs, r)
			if r.ExternalVersion > newest {
				newest = r.ExternalVersion
			}
		}
		if len(recs) > 0 {
			if _, err := submit(channel.KindOrder, recs); err != nil {
				return total, err
			}
			total += len(recs)
		}
		token = page.Next
		if token == "" {
			break
		}
		if !cursorRe.MatchString(token) {
			return total, errors.New("channel sent a page token that is not a token")
		}
	}
	if token == "" && newest != "" && newest != since {
		if err := channel.CursorSet(ordersCursor, []byte(newest)); err != nil {
			return total, err
		}
	}
	return total, nil
}
