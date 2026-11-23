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
	// Outs is the canonical JSON of each output artifact in Arts, in the same order.
	Outs []string `json:"outs,omitempty"`
	// Needs names the jobs whose artifacts a download loaded for a need that the IR holds without artifacts.
	Needs []string `json:"needs,omitempty"`
	// Seq orders the steps that stand at the same place.
	Seq int `json:"seq,omitempty"`
}

func carryYAML(v any) (string, error) {
	b, err := yaml.Marshal(v)
	return string(b), err
}

// withoutBookkeeping drops the Ext keys that a parser sets only to rebuild its own layout, and the Ext of other
// providers, which round-trip metadata keeps.
func withoutBookkeeping(x ir.Ext) ir.Ext {
	var out ir.Ext
	for prov, raw := range x {
		if prov != dialect {
			continue
		}
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
	// Outs is the canonical JSON of each output artifact in Names, in the same order.
	Outs []string `json:"outs,omitempty"`
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
	var names, outs []string
	taken := map[string]bool{}
	for _, n := range e.uploadsOf(&ir.Job{Steps: j.Steps}) {
		taken[n] = true
	}
	for _, a := range j.Artifacts {
		paths := a.Paths
		if len(paths) == 0 {
			paths = []string{safeName(a.Name)}
		}
		for i, n := range outputNames(taken, safeName(a.Name), a.Paths) {
			names = append(names, n)
			outs = append(outs, canonJSON(map[string]any{"name": n, "path": artifactPath(paths[i])}))
		}
	}
	if err := e.markArg(caps.JobArtifacts, path, artifactsArg{Arts: body, Names: names, Outs: outs}); err != nil {
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

// imagesArg holds where the IR put the images when that is not where a reader of the file puts them. A file
// whose containers share one image reads as a job image, and one with several reads as step images.
type imagesArg struct {
	// Steps holds the step image of each step as IR YAML.
	Steps map[string]string `json:"steps,omitempty"`
	// Wrote holds the image reference of the IR that each script step runs.
	Wrote map[string]string `json:"wrote"`
	// Job is the job image as IR YAML, or empty.
	Job string `json:"job,omitempty"`
}

// readImage is the image that a reader of the file gives a container that runs ref: the default image is no image.
func readImage(ref string) string {
	if ref == defaultImage {
		return ""
	}
	return ref
}

// irImage is the image reference of the IR that a container runs: its own step image, then the job image. A
// container with no script step runs the image that it was written with.
func irImage(j *ir.Job, c *container) string {
	if st := c.st; st != nil && st.Kind != ir.StepAction {
		if st.Image != nil {
			return st.Image.Ref
		}
		if j.Container != nil {
			return j.Container.Ref
		}
	}
	return c.image
}

// imageLayoutMatches is true when a reader of the file puts the images where the IR has them.
func imageLayoutMatches(j *ir.Job, conts []*container) bool {
	refs := map[string]bool{}
	for _, c := range conts {
		refs[irImage(j, c)] = true
	}
	for _, c := range conts {
		st := c.st
		if st == nil || st.Kind == ir.StepAction {
			continue
		}
		var want string
		if st.Image != nil {
			want = st.Image.Ref
		}
		if len(refs) == 1 && want != "" || len(refs) > 1 && want != readImage(irImage(j, c)) {
			return false
		}
	}
	var job string
	if j.Container != nil {
		job = j.Container.Ref
	}
	if len(refs) == 1 {
		for ref := range refs {
			return job == readImage(ref)
		}
	}
	return job == ""
}

// markImages writes the image layout of a job whose file would read back with the images elsewhere.
func (e *emitter) markImages(j *ir.Job, path string, conts []*container) {
	if len(conts) == 0 || imageLayoutMatches(j, conts) {
		return
	}
	arg := imagesArg{Steps: map[string]string{}, Wrote: map[string]string{}}
	var err error
	if j.Container != nil {
		if arg.Job, err = carryYAML(j.Container); err != nil {
			e.lose(path, caps.StepImage, "the image marker cannot be written: "+err.Error())
			return
		}
	}
	for _, c := range conts {
		st := c.st
		if st == nil || st.Kind == ir.StepAction {
			continue
		}
		arg.Wrote[st.ID] = irImage(j, c)
		if st.Image == nil {
			continue
		}
		if arg.Steps[st.ID], err = carryYAML(st.Image); err != nil {
			e.lose(path, caps.StepImage, "the image marker cannot be written: "+err.Error())
			return
		}
	}
	if err := e.markArg(caps.StepImage, path, arg); err != nil {
		e.lose(path, caps.StepImage, "the image marker cannot be written: "+err.Error())
	}
}

// nameArg holds the display name of a job or a step, which Argo has no field for.
type nameArg struct {
	Name string `json:"name"`
}

func (e *emitter) markName(path, name string) {
	if name == "" {
		return
	}
	if err := e.markArg(caps.DisplayName, path, nameArg{Name: name}); err != nil {
		e.lose(path, caps.DisplayName, "the name marker cannot be written: "+err.Error())
	}
}
