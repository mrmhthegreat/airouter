// Command airouter is the CLI entry point. All subcommand logic lives in
// github.com/mrmhthegreat/airouter/pkg/clicmd — this file just dispatches.
//
// Build a binary from this package and you get the standalone airouter CLI.
// Import the clicmd package from any other Go program and you get the same
// subcommands as Go functions (clicmd.Provider, clicmd.Bootstrap, etc.).
package main

import (
	"os"

	"github.com/mrmhthegreat/airouter/pkg/clicmd"
)

func main() {
	clicmd.Dispatch(os.Args[1:])
}
