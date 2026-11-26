package gitlab

import (
	"bytes"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/inline"
	"github.com/lukaszraczylo/pipemaster/report"
)

// maxChildDepth bounds the nesting of child pipelines that the parser follows; GitLab allows two levels.
const maxChildDepth = 4

// childRoot names the synthetic root file of a child; it cannot clash with a file of the repository.
const childRoot = ".pipemaster/child.gitlab-ci.yml"

const (
	extTrigger      = "trigger"
	triggerInclude  = "include"
	includeLocalKey = "local"
	triggerNote     = "trigger jobs start child or multi-project pipelines"
)

// attachChildren reads the child pipeline of each trigger job that includes only local files, which exist in
// fsys, and holds it in the job. Any other trigger keeps its fail-loud form.
func (a adapter) attachChildren(fsys fs.FS, proj *ir.Project, rep *report.Report, chain []string) error {
	if len(chain) > maxChildDepth {
		return nil
	}
	for _, pl := range proj.Pipelines {
		for _, j := range pl.Jobs {
			own, _ := j.Ext[dialect].(map[string]any)
			trig, _ := own[extTrigger].(map[string]any)
			files, ok := localFiles(fsys, trig)
			if !ok {
				continue
			}
			if slices.ContainsFunc(files, func(f string) bool { return slices.Contains(chain, f) }) {
				rep.Add(report.Entry{
					IRPath: "pipeline/" + pl.ID + "/job/" + j.ID, Feature: caps.ExtPassthrough, Level: caps.Lost,
					Strategy: "kept in Ext[gitlab]",
					Detail:   "the child pipeline " + strings.Join(files, ", ") + " includes a file that starts it again, so it runs without end; the converter does not unroll it and its work is missing",
				})
				continue
			}
			child, crep, gate, cfiles, err := a.parseChild(fsys, files, chain)
			if err != nil {
				rep.Add(report.Entry{
					IRPath: "pipeline/" + pl.ID + "/job/" + j.ID, Feature: caps.ExtPassthrough, Level: caps.Lost,
					Strategy: "kept in Ext[gitlab]",
					Detail:   "the child pipeline " + strings.Join(files, ", ") + " could not be read, so its work is missing: " + err.Error(),
				})
				continue
			}
			if child == nil {
				continue
			}
			child.ID = pl.ID + "-" + j.ID
			rep.Entries = slices.DeleteFunc(rep.Entries, func(e report.Entry) bool {
				return e.IRPath == "pipeline/"+pl.ID+"/job/"+j.ID && e.Feature == caps.ExtPassthrough && e.Detail == triggerNote
			})
			crep.Entries = slices.DeleteFunc(crep.Entries, isChildWorkflowNote)
			rep.Entries = append(rep.Entries, inline.RemapPaths(crep.Entries, pipelineID, pl.ID, j.ID)...)
			j.Child = &ir.Child{Gate: gate, Pipeline: child, Native: []string{dialect}, Files: files}
			for _, f := range cfiles {
				if !slices.Contains(proj.Origin.Files, f) {
					proj.Origin.Files = append(proj.Origin.Files, f)
				}
			}
		}
	}
	return nil
}

// isChildWorkflowNote is true for the note that workflow:rules have no trigger form. A child has no trigger: its
// rules run on the parent_pipeline source, and the parser already folded them into the conditions of its jobs.
func isChildWorkflowNote(e report.Entry) bool {
	return e.IRPath == "pipeline/"+pipelineID && strings.HasPrefix(e.Strategy, "kept in Ext[gitlab][workflow]")
}

// localFiles returns the files that a trigger includes when every one is a literal local path that exists. A
// project, template, remote or artifact include, an include with rules or inputs, and a glob give false.
func localFiles(fsys fs.FS, trig map[string]any) ([]string, bool) {
	if trig == nil {
		return nil, false
	}
	if _, cross := trig["project"]; cross {
		return nil, false
	}
	var raw []any
	switch v := trig[triggerInclude].(type) {
	case nil:
		return nil, false
	case []any:
		raw = v
	default:
		raw = []any{v}
	}
	var files []string
	for _, r := range raw {
		var p string
		switch v := r.(type) {
		case string:
			p = v
		case map[string]any:
			s, _ := v[includeLocalKey].(string)
			if len(v) != 1 || s == "" {
				return nil, false
			}
			p = s
		default:
			return nil, false
		}
		clean, ok := localPath(fsys, p)
		if !ok {
			return nil, false
		}
		files = append(files, clean)
	}
	return files, len(files) > 0
}

func localPath(fsys fs.FS, p string) (string, bool) {
	if strings.ContainsAny(p, "*?") || slices.Contains(strings.Split(p, "/"), "..") {
		return "", false
	}
	clean := strings.TrimPrefix(path.Clean("/"+p), "/")
	if clean == "" || !fs.ValidPath(clean) {
		return "", false
	}
	st, err := fs.Stat(fsys, clean)
	return clean, err == nil && !st.IsDir()
}

// parseChild reads files as one child pipeline: GitLab merges the files of one include list, so they become the
// includes of a synthetic root file. It returns the files the child read.
func (a adapter) parseChild(fsys fs.FS, files []string, chain []string) (*ir.Pipeline, *report.Report, expr.Expr, []string, error) {
	var inc []map[string]string
	for _, f := range files {
		inc = append(inc, map[string]string{includeLocalKey: "/" + f})
	}
	root, err := yaml.Marshal(map[string]any{triggerInclude: inc})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	proj, rep, gate, err := a.parseRoot(overlayFS{FS: fsys, name: childRoot, data: root}, childRoot, append(slices.Clone(chain), files...))
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(proj.Pipelines) != 1 {
		return nil, nil, nil, nil, nil
	}
	read := slices.DeleteFunc(slices.Clone(proj.Origin.Files), func(f string) bool { return f == childRoot })
	return proj.Pipelines[0], rep, gate, read, nil
}

// overlayFS serves one synthetic file over a real tree, so the include loader reads the child as a root file.
type overlayFS struct {
	fs.FS
	name string
	data []byte
}

func (o overlayFS) Open(name string) (fs.File, error) {
	if name != o.name {
		return o.FS.Open(name)
	}
	return &overlayFile{Reader: bytes.NewReader(o.data), name: o.name, size: int64(len(o.data))}, nil
}

type overlayFile struct {
	*bytes.Reader
	name string
	size int64
}

func (f *overlayFile) Stat() (fs.FileInfo, error) { return f, nil }
func (f *overlayFile) Close() error               { return nil }
func (f *overlayFile) Name() string               { return f.name }
func (f *overlayFile) Size() int64                { return f.size }
func (f *overlayFile) Mode() fs.FileMode          { return 0o444 }
func (f *overlayFile) ModTime() time.Time         { return time.Time{} }
func (f *overlayFile) IsDir() bool                { return false }
func (f *overlayFile) Sys() any                   { return nil }

var _ io.Reader = (*overlayFile)(nil)
