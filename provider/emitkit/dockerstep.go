package emitkit

import (
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// DockerRewrite is the strategy of the report entry for a docker:// step that became a script step.
const DockerRewrite = "docker step as image step"

// Inputs of a docker:// step that run the image: the runner starts it with this entrypoint and these arguments.
const (
	dockerWithEntrypoint = "entrypoint"
	dockerWithArgs       = "args"
)

// DockerSteps returns p with each docker:// action step that only sets an entrypoint and arguments rewritten as the
// script step that runs the command line, which every target has a form for. The third result holds the rewritten
// steps as the source had them, by IR path. It returns p itself when there is none, and never changes p. A step with any other input, or an input that is not literal text, stays an action step.
// emptyEntrypoint is true for a target that runs the image entrypoint before a script (GitLab): the step then clears it.
// Other targets run the script in place of the entrypoint, so the step names none.
func DockerSteps(p *ir.Project, emptyEntrypoint bool) (*ir.Project, []report.Entry, map[string]*ir.Step) {
	if p == nil || !slices.ContainsFunc(p.Pipelines, hasDockerStep) {
		return p, nil, nil
	}
	out := ir.Clone(p)
	var entries []report.Entry
	orig := map[string]*ir.Step{}
	for _, pl := range out.Pipelines {
		if pl == nil {
			continue
		}
		for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
			if j == nil {
				continue
			}
			for _, st := range j.Steps {
				if !dockerStep(st) {
					continue
				}
				img, ok := dockerImage(st)
				if !ok {
					continue
				}
				path := "pipeline/" + pl.ID + "/job/" + j.ID + "/step/" + st.ID
				orig[path] = &ir.Step{Kind: st.Kind, Action: st.Action, With: st.With}
				entries = append(entries, report.Entry{
					IRPath: path, Feature: caps.StepAction,
					Level: caps.Emulated, Strategy: DockerRewrite,
					Detail: "the step runs the image " + st.Action.Image + " with its entrypoint and arguments as one command line in a script step with no image entrypoint; the image needs a shell, and the step runs in the job workspace, not in the container layout that a docker action has; a return without the round-trip metadata keeps the script step",
				})
				if !emptyEntrypoint {
					img.Entrypoint = nil
				}
				st.Kind, st.Image, st.Script, st.Action, st.With = ir.StepScript, img.Image, img.Script, nil, nil
			}
		}
	}
	return out, entries, orig
}

func hasDockerStep(pl *ir.Pipeline) bool {
	if pl == nil {
		return false
	}
	for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		if j != nil && slices.ContainsFunc(j.Steps, dockerStep) {
			return true
		}
	}
	return false
}

func dockerStep(st *ir.Step) bool {
	return st != nil && st.Kind == ir.StepAction && st.Action != nil && st.Action.Kind == ir.ActionDocker
}

// dockerRun is the image step a docker step becomes: the image with no entrypoint and the command line as its script.
type dockerRun struct {
	*ir.Image
	Script []string
}

// dockerImage builds the image step of a docker step, or reports false when the step carries more than a command.
func dockerImage(st *ir.Step) (dockerRun, bool) {
	text := map[string]string{}
	for k, v := range st.With {
		if v.Kind != ir.ValueLit || (k != dockerWithEntrypoint && k != dockerWithArgs) {
			return dockerRun{}, false
		}
		text[k] = v.Lit
	}
	if len(st.Script) > 0 || st.Image != nil {
		return dockerRun{}, false
	}
	img := &ir.Image{Ref: st.Action.Image}
	if text[dockerWithEntrypoint] == "" {
		// The image's own entrypoint runs, and a script step cannot name it: the step stays an action and the
		// emitter reports it lost, as it does for a plugin image it does not know.
		return dockerRun{}, false
	}
	words, ok := splitWords(text[dockerWithArgs])
	if !ok {
		return dockerRun{}, false
	}
	argv := make([]string, 0, len(words)+1)
	for _, w := range append([]string{text[dockerWithEntrypoint]}, words...) {
		argv = append(argv, quoteWord(w))
	}
	img.Entrypoint = []string{""}
	return dockerRun{Image: img, Script: []string{strings.Join(argv, " ")}}, true
}

// quoteWord writes one word so a shell passes it on unchanged.
func quoteWord(w string) string {
	if w != "" && strings.Trim(w, safeWord) == "" {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

// splitWords splits an arguments string the way the runner does: on blanks, with single quotes, double quotes
// and backslash escapes.
func splitWords(s string) ([]string, bool) {
	var words []string
	var cur strings.Builder
	in, esc := false, false
	quote := rune(0)
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\\':
			esc, in = true, true
		case quote == '"':
			if r == '"' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 || esc {
		return nil, false
	}
	if in {
		words = append(words, cur.String())
	}
	return words, true
}

// DockerEmitted reports whether st, a step as a target file parses back, is still the script step that DockerSteps
// wrote for the stored docker step orig.
func DockerEmitted(st, orig *ir.Step) bool {
	if st == nil || orig == nil || st.Image == nil {
		return false
	}
	run, ok := dockerImage(&ir.Step{Kind: orig.Kind, Action: orig.Action, With: orig.With})
	return ok && st.Action == nil && st.Image.Ref == run.Ref && (len(st.Image.Entrypoint) == 0 || slices.Equal(st.Image.Entrypoint, run.Entrypoint)) &&
		slices.Equal(st.Script, run.Script)
}
