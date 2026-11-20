package gha

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

var (
	yamlLineRE = regexp.MustCompile(`^yaml: (?:unmarshal errors:\s*)?(?:line (\d+): )?`)
	irPathRE   = regexp.MustCompile(`^pipeline/([^/]+)(?:/job/([^/]+))?`)
)

// Parse reads every workflow file in the dialect's first existing directory into one pipeline each; the pipeline ID is the file stem.
func (a *adapter) Parse(fsys fs.FS) (*ir.Project, *report.Report, error) {
	files, err := a.readDir(fsys)
	if err != nil {
		return nil, nil, err
	}
	rep := &report.Report{Source: a.d.ID}
	proj, scan, err := a.parseFiles(fsys, files, rep, true)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case !scan.restored():
	case scan.mixed():
		// Restored conditions read in their source dialect and native ones in GHA's, and the project holds one
		// dialect, so no file restores its source conditions.
		*rep = report.Report{Source: a.d.ID}
		if proj, _, err = a.parseFiles(fsys, files, rep, false); err != nil {
			return nil, nil, err
		}
		for _, f := range scan.restoredFiles {
			rep.Add(report.Entry{
				Feature: caps.ExprOpaque,
				Level:   caps.Lost,
				Detail:  scan.mixedDetail(f, a.d.ID),
			})
		}
	default:
		// A restored condition reads in its source dialect, so a later emit must translate it again.
		proj.Origin.Provider, proj.Origin.Dialect = scan.origins[0], scan.origins[0]
	}
	return proj, rep, nil
}

// condScan records which files restored their source conditions and which hold conditions that stayed native.
type condScan struct {
	restoredFiles []string
	// origins holds the source dialect of each restored file, in the order of restoredFiles.
	origins     []string
	nativeConds bool
}

func (c condScan) restored() bool { return len(c.restoredFiles) > 0 }
func (c condScan) mixed() bool    { return c.restored() && (c.nativeConds || !c.oneOrigin()) }

func (c condScan) oneOrigin() bool {
	return !slices.ContainsFunc(c.origins, func(o string) bool { return o != c.origins[0] })
}

func (c condScan) mixedDetail(file, dialect string) string {
	if c.nativeConds {
		return fmt.Sprintf("source conditions of %s not restored: the directory also holds native %s conditions", file, dialect)
	}
	return fmt.Sprintf("source conditions of %s not restored: the directory holds conditions from more than one source dialect", file)
}

// parseFiles parses each file into one pipeline. With restoreConds false, source condition markers are ignored.
func (a *adapter) parseFiles(fsys fs.FS, files []string, rep *report.Report, restoreConds bool) (*ir.Project, condScan, error) {
	var scan condScan
	proj := &ir.Project{Origin: ir.Origin{Provider: a.d.ID, Dialect: a.d.ID, Files: files}}
	byStem := map[string]string{}
	lines := map[string]int{}
	for _, f := range files {
		stem := strings.TrimSuffix(path.Base(f), path.Ext(f))
		if prev, dup := byStem[stem]; dup {
			return nil, scan, fmt.Errorf("%s: %s and %s share the pipeline id %q", a.d.ID, prev, f, stem)
		}
		byStem[stem] = f
		src, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, scan, fmt.Errorf("%s: %w", a.d.ID, err)
		}
		fp := &fileParser{d: a.d, dialect: provider.Family(a.d.ID), stem: stem, file: f, rep: rep, lines: lines}
		pl, err := fp.parseWorkflow(src)
		if err != nil {
			return nil, scan, err
		}
		if from := collapseMarkers(&emitter{d: a.d, led: emitkit.NewLedger("", a.d.ID, a.Caps())}, pl, emitkit.ParseMarkers(src), emitkit.ParseArgs(src), rep, restoreConds); from != "" {
			scan.restoredFiles = append(scan.restoredFiles, f)
			scan.origins = append(scan.origins, from)
		} else if hasConds(pl) {
			scan.nativeConds = true
		}
		proj.Pipelines = append(proj.Pipelines, pl)
	}
	if err := ir.Validate(proj); err != nil {
		return nil, scan, locate(err, byStem, lines)
	}
	ir.Normalize(proj)
	return proj, scan, nil
}

func hasConds(pl *ir.Pipeline) bool {
	for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		if j != nil && (j.If != nil || slices.ContainsFunc(j.Steps, func(st *ir.Step) bool { return st.If != nil })) {
			return true
		}
	}
	return false
}

// locate prefixes each validation error with the file and line of the job it names.
func locate(err error, byStem map[string]string, lines map[string]int) error {
	var errs []error
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs = j.Unwrap()
	} else {
		errs = []error{err}
	}
	out := make([]error, 0, len(errs))
	for _, e := range errs {
		out = append(out, fmt.Errorf("%s: %w", position(e, byStem, lines), e))
	}
	return errors.Join(out...)
}

func position(err error, byStem map[string]string, lines map[string]int) string {
	var ie *ir.Error
	if !errors.As(err, &ie) {
		return "gha"
	}
	m := irPathRE.FindStringSubmatch(ie.Path)
	if m == nil {
		return "gha"
	}
	stem, job := m[1], m[2]
	if job == "" {
		job = firstCycleJob(ie.Msg)
	}
	if line, ok := lines[stem+"/"+job]; ok {
		return fmt.Sprintf("%s:%d", byStem[stem], line)
	}
	return byStem[stem]
}

// firstCycleJob extracts the first job of a "needs cycle: a -> b -> a" message.
func firstCycleJob(msg string) string {
	rest, ok := strings.CutPrefix(msg, "needs cycle: ")
	if !ok {
		return ""
	}
	first, _, _ := strings.Cut(rest, " -> ")
	return first
}

func (fp *fileParser) parseWorkflow(src []byte) (*ir.Pipeline, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty workflow", fp.file)
		}
		return nil, fp.yamlErr(err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, fp.errf(&extra, "workflow files hold one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return nil, fp.yamlErr(err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, fmt.Errorf("%s: empty workflow", fp.file)
	}
	m, err := fp.mapping(doc.Content[0], "workflow")
	if err != nil {
		return nil, err
	}
	pl := &ir.Pipeline{ID: fp.stem}
	if err := fp.pipelineFields(pl, m); err != nil {
		return nil, err
	}
	defaults, err := fp.takeRunDefaults(m)
	if err != nil {
		return nil, err
	}
	if jn := m.take("jobs"); jn != nil {
		if err := fp.parseJobs(pl, jn, defaults); err != nil {
			return nil, err
		}
	}
	pl.Ext, err = m.ext(fp.d.Extra[NodeWorkflow])
	return pl, err
}

// yamlErr rewrites a YAML syntax error as <file>:<line>: <message>.
func (fp *fileParser) yamlErr(err error) error {
	msg := err.Error()
	loc := yamlLineRE.FindStringSubmatch(msg)
	msg = strings.TrimSpace(yamlLineRE.ReplaceAllString(msg, ""))
	if loc != nil && loc[1] != "" {
		return fmt.Errorf("%s:%s: %s", fp.file, loc[1], msg)
	}
	return fmt.Errorf("%s: %s", fp.file, msg)
}

func (fp *fileParser) parseJobs(pl *ir.Pipeline, n *yaml.Node, defaults runDefaults) error {
	jm, err := fp.mapping(n, "jobs")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range jm.entries {
		if seen[e.key] {
			return fp.errf(e.val, "duplicate job %q", e.key)
		}
		seen[e.key] = true
		fp.lines[fp.stem+"/"+e.key] = e.line
		j, err := fp.parseJob(e.key, e.val, defaults)
		if err != nil {
			return err
		}
		pl.Jobs = append(pl.Jobs, j)
	}
	return nil
}
