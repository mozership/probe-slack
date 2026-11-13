// Command probe-slack serves a Probe action that posts a message to a
// Slack channel: the notes of a run, a text the workflow writes, or both.
// Probe starts it for a step that uses github.com/mozership/probe-slack@<commit>.
package main

import (
	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe/actionrpc"
)

// version is set at build time.
var version = "dev"

func main() {
	actionrpc.Serve(func(log hclog.Logger) actionrpc.Action {
		return &Action{log: log}
	})
}
