package cli

import (
	"flag"
	"fmt"
	"io"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const inspectUsage = "usage: pipemaster inspect <dir> [--from <provider>]\n"

func runInspect(args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("inspect", flag.ContinueOnError)
	from := fset.String("from", "", "source provider")
	pos, ok := parseArgs(fset, args, 1, inspectUsage, stderr)
	if !ok {
		return exitUsage
	}
	src, fsys, code, ok := resolveSource(pos[0], *from, stderr)
	if !ok {
		return code
	}
	proj, _, err := src.Parse(fsys)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "parse %s: %v\n", src.ID(), err)
		return exitError
	}
	b, err := ir.Marshal(proj)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "marshal IR: %v\n", err)
		return exitError
	}
	_, _ = stdout.Write(b)
	return exitOK
}
