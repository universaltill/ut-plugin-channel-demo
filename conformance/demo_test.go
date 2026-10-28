//go:build !wasip1

package conformance_test

import (
	"testing"

	"github.com/universaltill/ut-plugin-channel-demo/conformance"
	"github.com/universaltill/ut-plugin-channel-demo/internal/fakeshop"
	"github.com/universaltill/ut-plugin-channel-demo/src/app"
)

// TestDemoAdapterPassesTheSuite is how every adapter wires the suite into its
// CI: its handlers, its own fake shop, its manifest's credential header.
func TestDemoAdapterPassesTheSuite(t *testing.T) {
	conformance.Run(t, demoTarget())
}

func demoTarget() conformance.Target {
	probe := fakeshop.New(fakeshop.Options{})
	return conformance.Target{
		Handlers: app.Handlers(),
		NewShop:  func() conformance.Shop { return fakeshop.New(fakeshop.Options{}) },
		Account:  probe.Account(), CredentialHeader: fakeshop.Header, Credential: probe.Token(),
		StockVariant: "v-0001-1", // tracked, seeded by the fake shop
	}
}
