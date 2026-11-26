//go:build behave

package behave

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/lukaszraczylo/pipemaster/internal/behave/glchild"
)

// withChildren runs the same-repository child pipelines that the trigger jobs of a file start, and returns the file
// with each trigger job replaced by a job that prints the markers of its child (see package glchild for the rules and
// their sources). vars are the variables of the pipeline that holds the file. A trigger that the harness cannot
// run blocks the cell with the cited reason.
func (e *gitlabEngine) withChildren(ctx context.Context, w *Workspace, doc []byte, vars map[string]string, depth int) ([]byte, string, error) {
	triggers, err := glchild.Triggers(doc, nil)
	if err != nil {
		return nil, "", blocked("the trigger jobs of the file cannot be read: %v", err)
	}
	if len(triggers) == 0 {
		return doc, "", nil
	}
	outcomes := map[string]glchild.Outcome{}
	var log strings.Builder
	for i, t := range triggers {
		if t.Reason != "" {
			return nil, "", blocked("%s", glChildWhy[t.Reason])
		}
		if depth >= glchild.MaxDepth {
			return nil, "", blocked("%s", glChildDepthWhy)
		}
		var files [][]byte
		for _, f := range t.Files {
			raw, err := os.ReadFile(filepath.Join(w.Dir, filepath.FromSlash(f))) // #nosec G304 -- an include file of the harness workspace
			if err != nil {
				return nil, "", blocked("the child file %s of trigger job %s is not in the output: %v", f, t.Job, err)
			}
			files = append(files, raw)
		}
		merged, err := glchild.Merge(files)
		if err != nil {
			return nil, "", blocked("the child files of trigger job %s cannot be merged: %v", t.Job, err)
		}
		childVars := maps.Clone(vars)
		childVars["CI_PIPELINE_SOURCE"] = glchild.Source
		maps.Copy(childVars, t.Vars)
		out, clog, err := e.runChild(ctx, w, merged, childVars, depth+1, fmt.Sprintf("%d-%d", depth, i))
		if err != nil {
			return nil, "", err
		}
		out.Depend = t.Depend
		outcomes[t.Job] = out
		log.WriteString(clog)
	}
	spliced, err := glchild.Rewrite(doc, outcomes)
	return spliced, log.String(), err
}

// runChild runs one child pipeline. GitLab creates no child when its workflow:rules match nothing, and the
// trigger job then fails (https://docs.gitlab.com/ci/pipelines/downstream_pipelines/).
func (e *gitlabEngine) runChild(ctx context.Context, w *Workspace, doc []byte, vars map[string]string, depth int, id string) (glchild.Outcome, string, error) {
	spliced, log, err := e.withChildren(ctx, w, doc, vars, depth)
	if err != nil {
		return glchild.Outcome{}, "", err
	}
	rewritten, err := e.rewriteImages(ctx, spliced)
	if err != nil {
		return glchild.Outcome{}, "", fmt.Errorf("prepare child images: %w", err)
	}
	admitted, err := workflowAdmits(rewritten, vars)
	if err != nil {
		return glchild.Outcome{}, "", blocked("workflow:rules of a child pipeline cannot be evaluated: %v", err)
	}
	if !admitted {
		return glchild.Outcome{}, log, nil
	}
	name := ".pm-child-" + id + ".yml"
	if err := os.WriteFile(filepath.Join(w.Dir, name), rewritten, 0o600); err != nil { // #nosec G703 -- a file of the harness workspace
		return glchild.Outcome{}, "", err
	}
	tr, err := e.exec(ctx, w, name, vars)
	if err != nil {
		return glchild.Outcome{}, "", err
	}
	return glchild.Outcome{Created: true, Exit: tr.Exit, Markers: tr.Markers}, log + tr.Log, nil
}
