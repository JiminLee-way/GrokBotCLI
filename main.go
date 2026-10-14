// gb — headless Grok Bot client for Linux servers (and macOS).
package main

import (
	"os"

	"gb/internal/cli"
)

func main() { os.Exit(cli.Run(os.Args[1:])) }
