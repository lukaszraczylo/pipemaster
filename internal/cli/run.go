// Package cli implements the pipemaster command-line entry point.
package cli

import (
	"fmt"
	"io"
)

// Version is overridden at build time via -ldflags.
var Version = "dev"

const (
	exitOK    = 0
	exitUsage = 2

	usageText = "usage: pipemaster <command>\n\ncommands:\n  version  print the pipemaster version\n"
)

// Run executes the CLI and returns the process exit code. Write errors on
// the std streams are ignored: there is no better channel to report them.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	switch args[0] {
	case "version":
		_, _ = fmt.Fprintf(stdout, "pipemaster %s\n", Version)
		return exitOK
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n%s", args[0], usageText)
		return exitUsage
	}
}
