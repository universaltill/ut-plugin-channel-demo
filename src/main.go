// Command plugin is the demo sales-channel adapter, built to
// bin/plugin.wasm (GOOS=wasip1 GOARCH=wasm) and run by the ut-cloud channel
// host for one ask per process (contract sales-channel-v1.md).
package main

import (
	"github.com/universaltill/universal-till/sdk/plugin/channel"

	"github.com/universaltill/ut-plugin-channel-demo/src/app"
)

func main() { channel.Run(app.Handlers()) }
