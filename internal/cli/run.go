// Package cli implements the pipemaster command-line entry point.
package cli

import (
	"fmt"
	"io"
	"runtime/debug"
)

const devVersion = "dev"

// Version is overridden at build time via -ldflags.
var Version = devVersion

// resolveVersion returns linked, or the module version of the build when linked is the dev default and
// the build records a tagged version (go install pkg@v1.2.3).
func resolveVersion(linked, module string) string {
	if linked == devVersion && module != "" && module != "(devel)" {
		return module
	}
	return linked
}

func buildVersion() string {
	module := ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		module = bi.Main.Version
	}
	return resolveVersion(Version, module)
}

// Exit codes; see the CLI section of the design.
const (
	exitOK     = 0
	exitError  = 1
	exitUsage  = 2
	exitStrict = 3
	exitStale  = 4

	usageText = `usage: pipemaster <command>

commands:
  version    print the pipemaster version
  detect     list the providers whose config exists in a directory
  inspect    print the canonical IR of a directory
  convert    convert a directory's pipelines to another provider
  roundtrip  convert there and back, and print any differences
  caps       compare two providers' feature support
  validate   check pipeline files against the provider schema
`
)

type command func(args []string, stdout, stderr io.Writer) int

var commands = map[string]command{
	"detect":    runDetect,
	"inspect":   runInspect,
	"convert":   runConvert,
	"roundtrip": runRoundtrip,
	"caps":      runCaps,
	"validate":  runValidate,
}

// Run executes the CLI and returns the process exit code. Write errors on
// the std streams are ignored: there is no better channel to report them.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	if args[0] == "version" {
		_, _ = fmt.Fprintf(stdout, "pipemaster %s\n", buildVersion())
		return exitOK
	}
	if c, ok := commands[args[0]]; ok {
		return c(args[1:], stdout, stderr)
	}
	_, _ = fmt.Fprintf(stderr, "unknown command %q\n%s", args[0], usageText)
	return exitUsage
}
