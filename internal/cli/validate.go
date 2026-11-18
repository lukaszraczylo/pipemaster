package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/validate"
)

const validateUsage = "usage: pipemaster validate <dir> --as <provider>\n"

func runValidate(args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("validate", flag.ContinueOnError)
	as := fset.String("as", "", "provider whose schema to validate against")
	pos, ok := parseArgs(fset, args, 1, validateUsage, stderr)
	if !ok {
		return exitUsage
	}
	if *as == "" {
		_, _ = fmt.Fprint(stderr, validateUsage)
		return exitUsage
	}
	pats := validate.Patterns(*as)
	if len(pats) == 0 {
		_, _ = fmt.Fprintf(stderr, "no schema for provider %q (validatable: %s)\n", *as, strings.Join(validate.Providers(), ", "))
		return exitUsage
	}

	fsys := os.DirFS(pos[0])
	seen := map[string]bool{}
	for _, pat := range pats {
		matches, err := fs.Glob(fsys, pat)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "glob %s: %v\n", pat, err)
			return exitError
		}
		for _, m := range matches {
			seen[m] = true
		}
	}
	if len(seen) == 0 {
		_, _ = fmt.Fprintf(stderr, "no files matching %s in %s\n", strings.Join(pats, ", "), pos[0])
		return exitError
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)

	code := exitOK
	for _, name := range names {
		if err := validateFile(fsys, *as, name); err != nil {
			_, _ = fmt.Fprintf(stderr, "%v\n", err)
			code = exitError
			continue
		}
		_, _ = fmt.Fprintf(stdout, "ok %s\n", name)
	}
	return code
}

func validateFile(fsys fs.FS, providerID, name string) error {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	if err := validate.File(providerID, name, b); err != nil && !errors.Is(err, validate.ErrNoSchema) {
		return err
	}
	return nil
}
