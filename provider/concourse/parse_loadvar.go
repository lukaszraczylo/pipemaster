package concourse

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// loadVarDir is the workspace directory that holds one file per load_var value.
const loadVarDir = ".pipemaster/vars"

var loadVarName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

// loadVarKeys are the keys of a load_var step that the script step carries.
var loadVarKeys = map[string]bool{"load_var": true, "file": true, "format": true, "reveal": true}

// loadVarFile is the file that holds the value of one load_var.
func loadVarFile(name string) string { return loadVarDir + "/" + name }

// loadVarStep reads a load_var step as a script step that copies the file into loadVarDir. Only the raw and trim
// formats and a file of the checkout or of a task output have a script form; any other load_var stays an opaque step.
func (jb *jobBuilder) loadVarStep(m *omap) *ir.Step {
	line := m.Lines["load_var"]
	name, _ := text(m.Vals["load_var"])
	file, _ := text(m.Vals["file"])
	opaque := func(why string) *ir.Step {
		return jb.opaque(m, "load_var", why, line)
	}
	for _, key := range m.Order {
		if !loadVarKeys[key] {
			return opaque("load_var has the key " + key + ", which has no script form")
		}
	}
	if !loadVarName.MatchString(name) {
		return opaque("the load_var name " + name + " is no plain file name")
	}
	format, _ := text(m.Vals["format"])
	if _, set := m.Vals["format"]; !set {
		format = "raw"
		switch strings.ToLower(path.Ext(file)) {
		case ".json":
			format = "json"
		case ".yml", ".yaml":
			format = "yaml"
		}
	}
	if format != "raw" && format != "trim" {
		return opaque("load_var format " + format + " needs a parser that no target script has; only raw and trim are written")
	}
	src, why := jb.artifactFile(file)
	if why != "" {
		return opaque(why)
	}
	out := shellQuote(loadVarFile(name))
	script := []string{"mkdir -p " + loadVarDir}
	if format == "raw" {
		script = append(script, "cat "+shellQuote(src)+" > "+out+" || exit 1")
	} else {
		script = append(script,
			"_pm_v=$(cat "+shellQuote(src)+") || exit 1",
			`_pm_v="${_pm_v#"${_pm_v%%[![:space:]]*}"}"`,
			`_pm_v="${_pm_v%"${_pm_v##*[![:space:]]}"}"`,
			`printf '%s' "$_pm_v" > `+out)
	}
	if jb.loadVars == nil {
		jb.loadVars = map[string]bool{}
	}
	jb.loadVars[name] = true
	st := jb.named(name)
	st.Kind = ir.StepScript
	st.Script = script
	st.Ext = ir.Ext{dialect: map[string]any{
		ExtKind: "load_var", ExtStep: plain(m), ExtLoadScript: stringsAny(script),
		emitkit.CarriedKey: []any{ExtKind, ExtStep, ExtLoadScript},
	}}
	jb.k.Rep.Add(jb.entry(caps.StepScript, caps.Emulated, "script step that writes "+loadVarFile(name),
		fmt.Sprintf("load_var %s has no variable form on the targets; a step copies %s (format %s) to %s and later steps read that file. A value read as json or yaml, and a field reference such as ((.:%s.key)), have no script form", name, src, format, loadVarFile(name), name), line))
	return st
}

// artifactFile turns <artifact>/<path> into the workspace path of the file, or says why no script can read it.
func (jb *jobBuilder) artifactFile(file string) (string, string) {
	art, rest, ok := strings.Cut(file, "/")
	if !ok || rest == "" || strings.Contains(file, varOpen) || slices.Contains(strings.Split(rest, "/"), "..") {
		return "", "the load_var file " + file + " is no plain artifact path"
	}
	if g, ok := jb.gets[art]; ok && jb.b.isRepository(g.res) {
		return rest, ""
	}
	if dir, ok := jb.outputs[art]; ok {
		return dir + "/" + rest, ""
	}
	return "", "the load_var file comes from artifact " + art + ", which is no checkout and no task output that the target runs"
}

func stringsAny(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

// loadRef is the script line that makes a load_var value the env var of a task.
func loadRef(env, name string) string {
	return "export " + env + `="$(cat ` + shellQuote(loadVarFile(name)) + `)"`
}
