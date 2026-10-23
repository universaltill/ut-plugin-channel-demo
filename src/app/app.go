package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/universaltill/universal-till/sdk/plugin/channel"
)

// Handlers is the adapter's whole surface: one handler per ask the manifest's
// capabilities need (contract section 4).
func Handlers() channel.Handlers {
	return channel.Handlers{
		channel.AskConnectTest:   connectTest,
		channel.AskOAuthCallback: oauthCallback,
		channel.AskWebhook:       webhook,
		channel.AskContact:       contact,
		channel.AskFetch:         fetch,
		channel.AskImport:        importCatalogue,
		channel.AskOutbox:        drainOutbox,
		channel.AskTick:          tick,
	}
}

// connectTest is read-only: it asks the shop who it is and counts, and
// writes nothing anywhere (conformance check 1).
func connectTest(channel.Event) (any, error) {
	var shop struct {
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		Locations []struct {
			Name string `json:"name"`
		} `json:"locations"`
		ProductCount int64 `json:"product_count"`
		OrderCount   int64 `json:"order_count"`
	}
	if err := getJSON("/api/shop", &shop); err != nil {
		return nil, err
	}
	out := channel.ConnectTestResult{AccountName: clip(shop.Name, 100), Scopes: shop.Scopes, Products: shop.ProductCount, Orders: shop.OrderCount}
	for _, l := range shop.Locations {
		out.Locations = append(out.Locations, clip(l.Name, 100))
	}
	return out, nil
}

// oauthCallback parses the parameters of an install request the HOST has
// already verified (signature, state, freshness) and whose code the host has
// already exchanged. It checks the shop domain once more and returns it.
func oauthCallback(ev channel.Event) (any, error) {
	var in channel.OAuthCallbackInput
	if err := ev.Decode(&in); err != nil {
		return nil, errors.New("callback parameters are not valid JSON")
	}
	shop := in.Params["shop"]
	if !accountRe.MatchString(shop) {
		return nil, errors.New("the shop is not an account of this channel")
	}
	out := channel.OAuthCallbackResult{Account: shop}
	for _, s := range strings.FieldsFunc(in.Params["scope"], func(r rune) bool { return r == ',' || r == ' ' }) {
		if len(s) > 100 {
			return nil, errors.New("a scope is too long")
		}
		out.Scopes = append(out.Scopes, s)
	}
	return out, nil
}

// webhook parses a webhook the host has verified. It has no tenant context:
// it makes no host call but Log, and says only what happened and to what.
func webhook(ev channel.Event) (any, error) {
	var in channel.WebhookInput
	if err := ev.Decode(&in); err != nil {
		return nil, errors.New("webhook input is not valid JSON")
	}
	var b struct {
		ID       string `json:"id"`
		Topic    string `json:"topic"`
		Shop     string `json:"shop"`
		Resource struct {
			ID string `json:"id"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(in.Body, &b); err != nil || b.ID == "" || !accountRe.MatchString(b.Shop) {
		return nil, errors.New("body is not a webhook of this channel")
	}
	out := channel.WebhookResult{Account: b.Shop, DedupeID: clip(b.ID, 128), Event: webhookEvent(b.Topic)}
	if b.Resource.ID != "" && okID(b.Resource.ID) {
		out.ResourceIDs = []string{b.Resource.ID}
	}
	return out, nil
}

// webhookEvent maps the channel's topic to the neutral event; the host never
// acts on a channel's own topic names.
func webhookEvent(topic string) string {
	switch topic {
	case "orders/create", "orders/updated":
		return channel.EventOrderChanged
	case "app/uninstalled":
		return channel.EventUninstalled
	case "customers/redact":
		return channel.EventCustomerRedact
	case "shop/redact":
		return channel.EventShopRedact
	case "customers/data_request":
		return channel.EventDataRequest
	}
	return channel.EventOther
}

// contact fetches an order's customer details live, for display only. The
// answer is never stored, submitted or logged.
func contact(ev channel.Event) (any, error) {
	var in channel.ContactInput
	if err := ev.Decode(&in); err != nil || !okID(in.OrderExternalID) {
		return nil, errors.New("not a valid order")
	}
	var c struct {
		Name, Email, Phone, Address string
	}
	var raw map[string]string
	if err := getJSON("/api/orders/"+esc(in.OrderExternalID)+"/contact", &raw); err != nil {
		return nil, err
	}
	c.Name, c.Email, c.Phone, c.Address = raw["name"], raw["email"], raw["phone"], raw["address"]
	return channel.ContactResult{Name: clip(c.Name, 200), Email: clip(c.Email, 200), Phone: clip(c.Phone, 50), Address: clip(c.Address, 500)}, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func logErr(code string, err error) {
	var se *statusError
	f := map[string]any{}
	if errors.As(err, &se) {
		f["status"] = se.Status
	} else {
		f["error"] = fmt.Sprintf("%T", err)
	}
	channel.Log(channel.LevelWarn, code, f)
}
