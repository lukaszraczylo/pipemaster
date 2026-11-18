package cli

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

const roundtripUsage = "usage: pipemaster roundtrip <dir> --via <provider> [--from <provider>]\n"

func runRoundtrip(args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("roundtrip", flag.ContinueOnError)
	via := fset.String("via", "", "intermediate provider")
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
	mid, ok := getProvider(*via, stderr)
	if !ok {
		return exitUsage
	}

	rep := &report.Report{Source: src.ID(), Target: src.ID()}
	orig, r, err := src.Parse(srcFS)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "parse %s: %v\n", src.ID(), err)
		return exitError
	}
	rep.Merge(r)

	midFS, err := hop(orig, nil, src.ID(), mid, rep, true)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return exitError
	}
	midProj, r, err := mid.Parse(midFS)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "parse %s: %v\n", mid.ID(), err)
		return exitError
	}
	rep.Merge(r)
	m, err := meta.Load(midFS)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load metadata: %v\n", err)
		return exitError
	}
	backFS, err := hop(midProj, m, "", src, rep, false)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return exitError
	}
	back, r, err := src.Parse(backFS)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "parse %s: %v\n", src.ID(), err)
		return exitError
	}
	rep.Merge(r)

	if err := rep.Render(stderr, "text"); err != nil {
		_, _ = fmt.Fprintf(stderr, "render report: %v\n", err)
		return exitError
	}
	if same, diff := ir.Equal(orig, back); !same {
		_, _ = fmt.Fprint(stdout, diff)
		return exitError
	}
	_, _ = fmt.Fprintln(stdout, "no differences")
	return exitOK
}

// hop emits proj through p into memory. Going out it attaches fresh metadata
// from sourceID; coming back it restores m, whose Source is the target.
func hop(proj *ir.Project, m *meta.Meta, sourceID string, p provider.Provider, rep *report.Report, attach bool) (fs.FS, error) {
	if m != nil {
		addOrphans(rep, roundtrip.Restore(proj, m))
	}
	files, r, err := p.Emit(proj)
	if err != nil {
		return nil, fmt.Errorf("emit %s: %w", p.ID(), err)
	}
	rep.Merge(r)
	if attach {
		files = meta.Attach(files, roundtrip.NewMeta(sourceID, proj, p, r))
	}
	out := fstest.MapFS{}
	for name, b := range files {
		out[name] = &fstest.MapFile{Data: b}
	}
	return out, nil
}
