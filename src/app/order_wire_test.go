//go:build !wasip1

package app_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/universaltill/universal-till/sdk/plugin/channel"

	"github.com/universaltill/ut-plugin-channel-demo/internal/fakeshop"
)

// The order records a tick submits, byte for byte. The golden file was written
// by the adapter's former local orderRecord type, before it moved to the SDK's channel.Order
// (ut-docs#4994), so a drift between the SDK type and the contract's record
// shows up here as a diff. Regenerate with UPDATE_GOLDEN=1 only for a change
// the contract itself makes.
func TestSubmittedOrderBytesAreStable(t *testing.T) {
	e := newEnv(t, fakeshop.Options{})
	e.ask(channel.AskTick, map[string]string{"event": "reconcile"})
	var got bytes.Buffer
	for _, r := range e.submitted(channel.KindOrder) {
		got.Write(r)
		got.WriteByte('\n')
	}
	const golden = "testdata/orders.golden.jsonl"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got.Len() == 0 || !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("submitted orders changed on the wire:\n got %s\nwant %s", got.Bytes(), want)
	}
}
