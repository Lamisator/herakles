// Command herakles is a deduplicating, compressing and encrypting backup
// system with optional client/server operation.
package main

import (
	"os"

	"github.com/lamisator/herakles/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
