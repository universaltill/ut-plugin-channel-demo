//go:build !wasip1

package app_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/sdk/plugin/channel"

	"github.com/universaltill/ut-plugin-channel-demo/internal/fakeshop"
)

// All ids are invented (ut-docs#4929).

func askRedact(e *env, topic string, body []byte) (channel.WebhookResult, error) {
	in := channel.WebhookInput{Headers: map[string]string{fakeshop.WebhookHeader: e.shop.SignWebhook(body)}, Body: body}
	r := e.em.Ask(channel.AskWebhook, in)
	var got channel.WebhookResult
	if r.Err != nil {
		return got, r.Err
	}
	return got, json.Unmarshal(r.Answer, &got)
}

func TestCustomerRedactCarriesOrderIDs(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	body := e.shop.RedactBody("evt-r1", "c-77", []string{"o-1", "o-2", "o-1"})
	got, err := askRedact(e, "customers/redact", body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Event != channel.EventCustomerRedact || len(got.ResourceIDs) != 1 || got.ResourceIDs[0] != "c-77" {
		t.Errorf("customer: %+v", got)
	}
	if strings.Join(got.OrderIDs, ",") != "o-1,o-2" {
		t.Errorf("order ids %v (deduplicated, in order)", got.OrderIDs)
	}
	got, err = askRedact(e, "customers/redact", e.shop.RedactBody("evt-r2", "c-77", nil))
	if err != nil || len(got.OrderIDs) != 0 {
		t.Errorf("no orders: %+v %v", got, err)
	}
}

func TestCustomerRedactRefusesBadOrderIDs(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	for _, bad := range []string{"", "../x", "a/b", "a b", "o?1", strings.Repeat("a", 129), "-lead"} {
		if _, err := askRedact(e, "customers/redact", e.shop.RedactBody("evt-b", "c-1", []string{"o-1", bad})); err == nil {
			t.Errorf("order id %q accepted", bad)
		}
	}
	for name, raw := range map[string]string{
		"number element": `{"id":"e","topic":"customers/redact","shop":"shop-a.demo-shop.example","resource":{"id":"c-1"},"orders_to_redact":[5]}`,
		"not an array":   `{"id":"e","topic":"customers/redact","shop":"shop-a.demo-shop.example","resource":{"id":"c-1"},"orders_to_redact":"o-1"}`,
	} {
		if _, err := askRedact(e, "customers/redact", []byte(raw)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCustomerRedactOrderLimit(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	mk := func(n int) []string {
		ids := make([]string, n)
		for i := range ids {
			ids[i] = "o-" + strconv.Itoa(i)
		}
		return ids
	}
	got, err := askRedact(e, "customers/redact", e.shop.RedactBody("evt-l", "c-1", mk(10000)))
	if err != nil || len(got.OrderIDs) != 10000 || got.OrderIDsTruncated {
		t.Fatalf("10,000: %d truncated=%v %v", len(got.OrderIDs), got.OrderIDsTruncated, err)
	}
	// 10,001 entries with one duplicate are 10,000 distinct: not truncated.
	dup := append([]string{"o-0"}, mk(10000)...)
	if got, err := askRedact(e, "customers/redact", e.shop.RedactBody("evt-l1", "c-1", dup)); err != nil || len(got.OrderIDs) != 10000 || got.OrderIDsTruncated {
		t.Errorf("10,001 with a duplicate: %d truncated=%v %v", len(got.OrderIDs), got.OrderIDsTruncated, err)
	}
	// 10,001 distinct: first 10,000 in payload order, flag set, resource kept.
	got, err = askRedact(e, "customers/redact", e.shop.RedactBody("evt-l2", "c-1", mk(10001)))
	if err != nil || len(got.OrderIDs) != 10000 || !got.OrderIDsTruncated {
		t.Fatalf("10,001: %d truncated=%v %v", len(got.OrderIDs), got.OrderIDsTruncated, err)
	}
	if got.OrderIDs[0] != "o-0" || got.OrderIDs[9999] != "o-9999" {
		t.Errorf("not the first 10,000 in order: %s .. %s", got.OrderIDs[0], got.OrderIDs[9999])
	}
	if len(got.ResourceIDs) != 1 || got.ResourceIDs[0] != "c-1" {
		t.Errorf("resource id lost: %v", got.ResourceIDs)
	}
	// A malformed id anywhere, even past the cap, still refuses the body.
	bad := append(mk(10005), "../x")
	if _, err := askRedact(e, "customers/redact", e.shop.RedactBody("evt-l3", "c-1", bad)); err == nil {
		t.Error("bad id past the cap accepted")
	}
}

func TestOtherTopicsCarryNoOrderIDs(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	for _, topic := range []string{"orders/updated", "shop/redact", "customers/data_request", "app/uninstalled", "something/else"} {
		body := e.shop.RedactBody("evt-"+topic, "c-1", []string{"o-1"})
		body = []byte(strings.Replace(string(body), `"topic":"customers/redact"`, `"topic":"`+topic+`"`, 1))
		got, err := askRedact(e, topic, body)
		if err != nil || len(got.OrderIDs) != 0 {
			t.Errorf("%s: %+v %v", topic, got, err)
		}
	}
}
