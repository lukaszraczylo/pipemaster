package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lukaszraczylo/pipemaster/provider"
)

const detectUsage = "usage: pipemaster detect <dir>\n"

func runDetect(args []string, stdout, stderr io.Writer) int {
	pos, ok := parseArgs(flag.NewFlagSet("detect", flag.ContinueOnError), args, 1, detectUsage, stderr)
	if !ok {
		return exitUsage
	}
	found := provider.Detect(os.DirFS(pos[0]))
	if len(found) == 0 {
		_, _ = fmt.Fprintf(stderr, "no supported pipeline config found in %s\n", pos[0])
		return exitError
	}
	for _, id := range found {
		_, _ = fmt.Fprintln(stdout, id)
	}
	return exitOK
}
