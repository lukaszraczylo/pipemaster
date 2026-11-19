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

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
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
	stack, err := meta.LoadStack(srcFS, proj.Origin.Files)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "load metadata: %v\n", err)
		return exitError
	}
	res, err := roundtrip.Hop(proj, stack, srcFS, src.ID(), tgt.ID(), *force, rep)
	if errors.Is(err, meta.ErrStale) {
		_, _ = fmt.Fprintf(stderr, "%v\nuse --force to convert anyway\n", err)
		return exitStale
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return exitError
	}
	files, emitRep, err := tgt.Emit(proj)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "emit %s: %v\n", tgt.ID(), err)
		return exitError
	}
	if !res.Restored {
		m := roundtrip.NewMeta(src.ID(), proj, tgt, emitRep)
		if err := roundtrip.RecordEmitted(m, tgt, files); err != nil {
			addMetaGap(emitRep, err)
		}
		res.Out.Layers = append(res.Out.Layers, m)
	}
	rep.Merge(emitRep)
	rep.Source, rep.Target = src.ID(), tgt.ID()

	if *embed {
		files = meta.EmbedStack(files, res.Out)
	} else {
		files = meta.AttachStack(files, res.Out)
	}
	if !validateAll(tgt.ID(), files, stderr) {
		return exitError
	}
	if err := writeFiles(*outDir, files); err != nil {
		_, _ = fmt.Fprintf(stderr, "write output: %v\n", err)
		return exitError
	}
	if !*embed && len(res.Out.Layers) == 0 {
		if err := removeStaleSidecar(*outDir, rep); err != nil {
			_, _ = fmt.Fprintf(stderr, "remove sidecar: %v\n", err)
			return exitError
		}
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

// addMetaGap reports that the metadata lacks its emitted-form record, so --force cannot restore lost fields
// over an edited file.
func addMetaGap(rep *report.Report, err error) {
	rep.Add(report.Entry{
		IRPath:  "",
		Feature: caps.ExtPassthrough,
		Level:   caps.Lost,
		Detail:  fmt.Sprintf("round-trip metadata has no record of the emitted form (%v); a later --force run keeps the edited file and restores no lost fields", err),
	})
}

// removeStaleSidecar deletes the sidecar a full return leaves behind in a reused output directory, since
// its stack describes files the conversion no longer produces.
func removeStaleSidecar(dir string, rep *report.Report) error {
	path := filepath.Join(dir, filepath.FromSlash(meta.SidecarPath))
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	rep.Add(report.Entry{
		Feature: caps.ExtPassthrough,
		Level:   caps.Native,
		Detail:  fmt.Sprintf("removed the stale round-trip sidecar %s: the conversion restored the origin", path),
	})
	sub := filepath.Dir(path)
	ents, err := os.ReadDir(sub)
	if err != nil || len(ents) > 0 {
		return err
	}
	return os.Remove(sub)
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
