package cli

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/lukaszraczylo/pipemaster/provider"
)

// parseArgs parses flags that may sit before or after positional arguments,
// since the documented form is `convert <dir> --to P`. It prints usage and
// returns ok=false when parsing fails or the positional count is not want.
func parseArgs(fset *flag.FlagSet, args []string, want int, usage string, stderr io.Writer) (pos []string, ok bool) {
	fset.SetOutput(io.Discard)
	rest := args
	for {
		if err := fset.Parse(rest); err != nil {
			_, _ = fmt.Fprintf(stderr, "%v\n%s", err, usage)
			return nil, false
		}
		if fset.NArg() == 0 {
			break
		}
		pos = append(pos, fset.Arg(0))
		rest = fset.Args()[1:]
	}
	if len(pos) != want {
		_, _ = fmt.Fprint(stderr, usage)
		return nil, false
	}
	return pos, true
}

// getProvider resolves id, reporting an unknown ID (with the valid ones) as a usage error.
func getProvider(id string, stderr io.Writer) (provider.Provider, bool) {
	p, err := provider.Get(id)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return nil, false
	}
	return p, true
}

// resolveSource picks the source provider from --from or by detection and
// returns the exit code to use when ok is false.
func resolveSource(dir, from string, stderr io.Writer) (p provider.Provider, fsys fs.FS, code int, ok bool) {
	fsys = os.DirFS(dir)
	if from != "" {
		p, ok = getProvider(from, stderr)
		return p, fsys, exitUsage, ok
	}
	found := provider.Detect(fsys)
	switch len(found) {
	case 0:
		_, _ = fmt.Fprintf(stderr, "no supported pipeline config found in %s\n", dir)
		return nil, nil, exitError, false
	case 1:
		p, ok = getProvider(found[0], stderr)
		return p, fsys, exitUsage, ok
	}
	_, _ = fmt.Fprintf(stderr, "several providers match %s: %s; pass --from to choose one\n", dir, strings.Join(found, ", "))
	return nil, nil, exitUsage, false
}
