package argo

import (
	"encoding/json"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// checkoutWrote marks a checkout that the file holds as a git artifact.
const checkoutWrote = "git-artifact"

// actionArg carries an action step, so the parser can put it back where it stood. The file holds the step as
// an input artifact, an output artifact, an image, a container or nothing.
type actionArg struct {
	// Step is the IR step as YAML text.
	Step string `json:"step"`
	// Wrote is checkoutWrote for a checkout that the file holds as a git artifact.
	Wrote string `json:"wrote,omitempty"`
	// Image is the image that a setup action gave to the containers after it.
	Image string `json:"image,omitempty"`
	// Kind is the registry kind of the action.
	Kind string `json:"kind,omitempty"`
	// Art is the git artifact that the emitter wrote for a checkout, as canonical JSON.
	Art string `json:"art,omitempty"`
	// Hold is the canonical JSON of the container that keeps the place of the step in the file.
	Hold string `json:"hold,omitempty"`
	// Arts names the output artifacts that an upload wrote.
	Arts []string `json:"arts,omitempty"`
	// Seq orders the steps that stand at the same place.
	Seq int `json:"seq,omitempty"`
}

func carryYAML(v any) (string, error) {
	b, err := yaml.Marshal(v)
	return string(b), err
}

// withoutBookkeeping drops the Ext keys that a parser sets only to rebuild its own layout.
func withoutBookkeeping(x ir.Ext) ir.Ext {
	var out ir.Ext
	for prov, raw := range x {
		m, ok := raw.(map[string]any)
		if !ok {
			out = setExt(out, prov, raw)
			continue
		}
		kept := map[string]any{}
		for k, v := range m {
			if !emitkit.BookkeepingExt[k] {
				kept[k] = v
			}
		}
		if len(kept) > 0 {
			out = setExt(out, prov, kept)
		}
	}
	return out
}

func setExt(e ir.Ext, k string, v any) ir.Ext {
	if e == nil {
		e = ir.Ext{}
	}
	e[k] = v
	return e
}

// outputsArg holds the values of the job outputs. A step that Argo cannot run, or an expression that has no
// spelling, leaves the value out of the file.
type outputsArg struct {
	Values map[string]string `json:"values"`
}

func (e *emitter) markOutputs(j *ir.Job, path string) {
	arg := outputsArg{Values: map[string]string{}}
	for _, o := range j.Outputs {
		if o.Value != "" {
			arg.Values[o.Name] = o.Value
		}
	}
	if len(arg.Values) == 0 {
		return
	}
	if err := e.markArg(caps.JobOutputs, path, arg); err != nil {
		e.lose(path, caps.JobOutputs, "the output marker cannot be written: "+err.Error())
	}
}

// workdirArg holds a relative working directory that the emitter wrote under the workspace.
type workdirArg struct {
	WorkDir string `json:"workdir"`
	Wrote   string `json:"wrote"`
}

// cacheArg holds the job caches as IR YAML. The file holds them as claims that carry no key or policy.
type cacheArg struct {
	Caches string `json:"caches"`
}

// artifactsArg holds the job artifacts as IR YAML and the names of the output artifacts that stand for them.
type artifactsArg struct {
	Arts  string   `json:"arts"`
	Names []string `json:"names"`
}

func (e *emitter) markCache(j *ir.Job, path string) {
	if len(j.Cache) == 0 {
		return
	}
	body, err := carryYAML(j.Cache)
	if err == nil {
		err = e.markArg(caps.JobCache, path, cacheArg{Caches: body})
	}
	if err != nil {
		e.lose(path, caps.JobCache, "the cache marker cannot be written: "+err.Error())
	}
}

func (e *emitter) markArtifacts(j *ir.Job, path string) {
	if len(j.Artifacts) == 0 {
		return
	}
	body, err := carryYAML(j.Artifacts)
	if err != nil {
		e.lose(path, caps.JobArtifacts, "the artifact marker cannot be written: "+err.Error())
		return
	}
	var names []string
	taken := map[string]bool{}
	for _, n := range e.uploadsOf(&ir.Job{Steps: j.Steps}) {
		taken[n] = true
	}
	for _, a := range j.Artifacts {
		names = append(names, outputNames(taken, safeName(a.Name), a.Paths)...)
	}
	if err := e.markArg(caps.JobArtifacts, path, artifactsArg{Arts: body, Names: names}); err != nil {
		e.lose(path, caps.JobArtifacts, "the artifact marker cannot be written: "+err.Error())
	}
}

// disabledArg holds the condition and the matrix of a job whose task the file disables with when false.
type disabledArg struct {
	Job string `json:"job"`
}

func (e *emitter) markDisabled(j *ir.Job, path string) {
	body, err := carryYAML(&ir.Job{If: j.If, Matrix: j.Matrix})
	if err == nil {
		err = e.markArg(caps.JobIf, path, disabledArg{Job: body})
	}
	if err != nil {
		e.lose(path, caps.JobIf, "the condition marker cannot be written: "+err.Error())
	}
}

// containerArg holds the job image with the entrypoint that the file cannot carry.
type containerArg struct {
	Image string `json:"image"`
	// Written is the image that the first container of the file got.
	Written string `json:"written,omitempty"`
}

// canonJSON is the one-line text that two equal values share, whatever the key order.
func canonJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func nodeCanon(n *yaml.Node) string {
	var v any
	if n.Decode(&v) != nil {
		return ""
	}
	return canonJSON(v)
}

// markContainer holds the job image when the file cannot: its entrypoint has no Argo form, or a setup action
// gave the containers another image.
func (e *emitter) markContainer(j *ir.Job, path, written string, replaced bool) {
	if j.Container == nil || (len(j.Container.Entrypoint) == 0 && !replaced) {
		return
	}
	if len(j.Container.Entrypoint) > 0 {
		e.lose(path, caps.JobContainer, "the entrypoint of the job image has no Argo form; the containers run sh -ec")
	}
	body, err := carryYAML(j.Container)
	if err == nil {
		err = e.markArg(caps.JobContainer, path, containerArg{Image: body, Written: written})
	}
	if err != nil {
		e.lose(path, caps.JobContainer, "the image marker cannot be written: "+err.Error())
	}
}
