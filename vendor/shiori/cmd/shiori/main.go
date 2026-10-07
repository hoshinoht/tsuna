// Command shiori is the workplan core CLI and the `shiori serve --stdio`
// native adapter protocol.
package main

import (
	"os"

	"github.com/hoshinoht/shiori/internal/cli"
)

func main() { os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr)) }
