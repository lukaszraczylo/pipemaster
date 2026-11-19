package cli

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

const roundtripUsage = "usage: pipemaster roundtrip <dir> --via <provider>[,<provider>...] [--from <provider>]\n"

func runRoundtrip(args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("roundtrip", flag.ContinueOnError)
	via := fset.String("via", "", "intermediate providers, comma separated")
	from := fset.String("from", "", "source provider")
	pos, ok := parseArgs(fset, args, 1, roundtripUsage, stderr)
	if !ok {
		return exitUsage
	}
	if *via == "" {
		_, _ = fmt.Fprint(stderr, roundtripUsage)
		return exitUsage
	}
	src, srcFS, code, ok := resolveSource(pos[0], *from, stderr)
	if !ok {
		return code
	}
	var mids []provider.Provider
	for _, id := range strings.Split(*via, ",") {
		mid, ok := getProvider(strings.TrimSpace(id), stderr)
		if !ok {
			return exitUsage
		}
		mids = append(mids, mid)
	}

	rep := &report.Report{Source: src.ID(), Target: src.ID()}
	orig, r, err := src.Parse(srcFS)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "parse %s: %v\n", src.ID(), err)
		return exitError
	}
	rep.Merge(r)

	cur, curFS, stack, prev := orig, srcFS, &meta.Stack{}, src
	for _, next := range append(mids, src) {
		cur, curFS, stack, err = hop(cur, curFS, stack, prev, next, rep)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%v\n", err)
			return exitError
		}
		prev = next
	}

	if err := rep.Render(stderr, "text"); err != nil {
		_, _ = fmt.Fprintf(stderr, "render report: %v\n", err)
		return exitError
	}
	if same, diff := ir.Equal(orig, cur); !same {
		_, _ = fmt.Fprint(stdout, diff)
		return exitError
	}
	_, _ = fmt.Fprintln(stdout, "no differences")
	return exitOK
}

// hop converts proj, parsed from fsys as from, to the provider to in memory. It returns the project that
// to's parser reads back and the metadata stack read back with it.
func hop(proj *ir.Project, fsys fs.FS, in *meta.Stack, from, to provider.Provider, rep *report.Report) (*ir.Project, fs.FS, *meta.Stack, error) {
	res, err := roundtrip.Hop(proj, in, fsys, from.ID(), to.ID(), false, rep)
	if err != nil {
		return nil, nil, nil, err
	}
	files, r, err := to.Emit(res.Proj)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("emit %s: %w", to.ID(), err)
	}
	rep.Merge(r)
	if !res.Restored {
		m := roundtrip.NewMeta(from.ID(), res.Proj, to, r)
		if err := roundtrip.RecordEmitted(m, to, files); err != nil {
			addMetaGap(rep, err)
		}
		res.Out.Layers = append(res.Out.Layers, m)
	}
	out := fstest.MapFS{}
	for name, b := range meta.AttachStack(files, res.Out) {
		out[name] = &fstest.MapFile{Data: b}
	}
	parsed, r, err := to.Parse(out)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse %s: %w", to.ID(), err)
	}
	rep.Merge(r)
	stack, err := meta.LoadStack(out, parsed.Origin.Files)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load metadata: %w", err)
	}
	return parsed, out, stack, nil
}
