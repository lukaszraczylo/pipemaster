package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"
)

const convertUsage = "usage: pipemaster convert <dir> --to <provider> [--from <provider>] [-o <dir>] [--report text|md|json] [--strict] [--embed-meta] [--force]\n"

// Output is written owner-only: converted pipelines can name secrets and registries.
const (
	dirMode  = 0o750
	fileMode = 0o600
)

// validateEmitted is a seam so tests can force a schema failure without a real schema-backed provider.
var validateEmitted = validate.File

var reportFormats = map[string]bool{"text": true, "md": true, "json": true}

func runConvert(args []string, _, stderr io.Writer) int {
	fset := flag.NewFlagSet("convert", flag.ContinueOnError)
	to := fset.String("to", "", "target provider")
	from := fset.String("from", "", "source provider")
	outDir := fset.String("o", ".", "output directory")
	format := fset.String("report", "text", "report format: text, md or json")
	strict := fset.Bool("strict", false, "exit 3 when any feature is not native")
	embed := fset.Bool("embed-meta", false, "embed round-trip metadata instead of writing the sidecar")
	force := fset.Bool("force", false, "ignore stale round-trip metadata")
	pos, ok := parseArgs(fset, args, 1, convertUsage, stderr)
	if !ok {
		return exitUsage
	}
	if *to == "" || !reportFormats[*format] {
		_, _ = fmt.Fprint(stderr, convertUsage)
		return exitUsage
	}
	src, srcFS, code, ok := resolveSource(pos[0], *from, stderr)
	if !ok {
		return code
	}
	tgt, ok := getProvider(*to, stderr)
	if !ok {
		return exitUsage
	}

	proj, rep, err := src.Parse(srcFS)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "parse %s: %v\n", src.ID(), err)
		return exitError
	}
	if rep == nil {
		rep = &report.Report{}
	}
	if code := applyStoredMeta(proj, rep, srcFS, tgt.ID(), *force, stderr); code != exitOK {
		return code
	}
	files, emitRep, err := tgt.Emit(proj)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "emit %s: %v\n", tgt.ID(), err)
		return exitError
	}
	rep.Merge(emitRep)
	rep.Source, rep.Target = src.ID(), tgt.ID()

	m := newMeta(src.ID(), proj)
	if *embed {
		files = meta.Embed(files, m)
	} else {
		files = meta.Attach(files, m)
	}
	if !validateAll(tgt.ID(), files, stderr) {
		return exitError
	}
	if err := writeFiles(*outDir, files); err != nil {
		_, _ = fmt.Fprintf(stderr, "write output: %v\n", err)
		return exitError
	}
	if err := rep.Render(stderr, *format); err != nil {
		_, _ = fmt.Fprintf(stderr, "render report: %v\n", err)
		return exitError
	}
	if *strict && rep.Worst() > caps.Native {
		return exitStrict
	}
	return exitOK
}

func newMeta(sourceID string, proj *ir.Project) *meta.Meta {
	return &meta.Meta{
		Source:  sourceID,
		Dialect: proj.Origin.Dialect,
		Ext:     meta.Collect(proj),
		Version: meta.Version,
	}
}

// applyStoredMeta restores round-trip metadata into proj when converting
// back to the provider that wrote it. Orphaned paths are added to rep.
func applyStoredMeta(proj *ir.Project, rep *report.Report, srcFS fs.FS, targetID string, force bool, stderr io.Writer) int {
	m, err := meta.Load(srcFS)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load metadata: %v\n", err)
		return exitError
	}
	if m == nil || m.Source != targetID {
		return exitOK
	}
	stale, err := m.Stale(srcFS)
	switch {
	case errors.Is(err, meta.ErrStale) && !force:
		_, _ = fmt.Fprintf(stderr, "round-trip metadata is stale; files changed since conversion: %s\nuse --force to convert anyway\n", strings.Join(stale, ", "))
		return exitStale
	case err != nil && !errors.Is(err, meta.ErrStale):
		_, _ = fmt.Fprintf(stderr, "check metadata: %v\n", err)
		return exitError
	}
	addOrphans(rep, meta.Reattach(proj, m))
	return exitOK
}

func addOrphans(rep *report.Report, orphans []string) {
	for _, path := range orphans {
		rep.Add(report.Entry{
			IRPath:  path,
			Feature: caps.ExtPassthrough,
			Level:   caps.Lost,
			Detail:  fmt.Sprintf("stored metadata for %s matches no node in the converted pipeline", path),
		})
	}
}

// validateAll checks every emitted file except the sidecar. A provider with
// no pinned schema only warns.
func validateAll(providerID string, files map[string][]byte, stderr io.Writer) bool {
	ok := true
	for _, name := range sortedNames(files) {
		if name == meta.SidecarPath {
			continue
		}
		err := validateEmitted(providerID, name, files[name])
		switch {
		case errors.Is(err, validate.ErrNoSchema):
			_, _ = fmt.Fprintf(stderr, "warning: no schema for %s; %s not validated\n", providerID, name)
		case err != nil:
			_, _ = fmt.Fprintf(stderr, "invalid output: %v\n", err)
			ok = false
		}
	}
	return ok
}

func writeFiles(dir string, files map[string][]byte) error {
	for _, name := range sortedNames(files) {
		if !fs.ValidPath(name) {
			return fmt.Errorf("emitter produced unsafe path %q", name)
		}
	}
	for _, name := range sortedNames(files) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), dirMode); err != nil {
			return err
		}
		if err := os.WriteFile(p, files[name], fileMode); err != nil {
			return err
		}
	}
	return nil
}

func sortedNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
