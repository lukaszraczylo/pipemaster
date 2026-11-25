package emitkit

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	// PluginEnvPrefix starts the variable a Drone or Woodpecker plugin reads each setting from.
	PluginEnvPrefix = "PLUGIN_"
	extSettings     = "settings"
	fromSecretKey   = "from_secret"
	listSeparator   = ","
)

// PluginRuns reports whether a step is a plugin that runs its image entrypoint: an image, settings and no script.
// The condition of the step is the caller's concern.
func PluginRuns(st *ir.Step) bool {
	return st.Kind == ir.StepPlugin && st.Action == nil && len(st.Script) == 0 &&
		st.Image != nil && st.Image.Ref != "" && len(st.Image.Entrypoint) == 0 && st.WorkDir == ""
}

// PluginEnvName is the variable that carries a setting: the upper-case name with dots and dashes as underscores.
func PluginEnvName(setting string) string {
	return PluginEnvPrefix + strings.Map(func(r rune) rune {
		if r == '-' || r == '.' {
			return '_'
		}
		return r
	}, strings.ToUpper(setting))
}

// PluginEnv returns the PLUGIN_ variables of a plugin step in name order, in the Woodpecker convention: a scalar
// setting is its text, a list of scalars is comma-joined, and any other list or map is JSON. The second result
// says why the settings cannot all be written; the variables are then nil.
func PluginEnv(st *ir.Step) ([]ir.Var, string) {
	byName := map[string]ir.Var{}
	owner := map[string]string{}
	add := func(setting string, v ir.Value) string {
		name := PluginEnvName(setting)
		if first, dup := owner[name]; dup {
			return "the settings " + first + " and " + setting + " both map to " + name
		}
		owner[name] = setting
		byName[name] = ir.Var{Name: name, Value: v}
		return ""
	}
	for _, setting := range slices.Sorted(maps.Keys(st.With)) {
		v := st.With[setting]
		if why := add(setting, v); why != "" {
			return nil, why
		}
	}
	for _, key := range slices.Sorted(maps.Keys(st.Ext)) {
		m, _ := st.Ext[key].(map[string]any)
		kept, _ := m[extSettings].(map[string]any)
		for _, setting := range slices.Sorted(maps.Keys(kept)) {
			raw := kept[setting]
			text, why := settingText(setting, raw)
			if why != "" {
				return nil, why
			}
			if why := add(setting, ir.Value{Lit: text}); why != "" {
				return nil, why
			}
		}
	}
	names := slices.Sorted(maps.Keys(byName))
	out := make([]ir.Var, len(names))
	for i, n := range names {
		out[i] = byName[n]
	}
	return out, ""
}

// settingText spells a setting that the IR holds only in Ext. A variable reference or a secret inside the value has
// no text the target would expand, so the setting has no spelling.
func settingText(setting string, raw any) (string, string) {
	if hasReference(raw) {
		return "", "the setting " + setting + " holds a variable or a secret inside a list or map"
	}
	if list, ok := raw.([]any); ok && allScalar(list) {
		parts := make([]string, 0, len(list))
		for _, e := range list {
			if e != nil {
				parts = append(parts, fmt.Sprint(e))
			}
		}
		return strings.Join(parts, listSeparator), ""
	}
	switch raw.(type) {
	case []any, map[string]any:
		b, err := json.Marshal(raw)
		if err != nil {
			return "", "the setting " + setting + " has no JSON form: " + err.Error()
		}
		return string(b), ""
	}
	return fmt.Sprint(raw), ""
}

func allScalar(list []any) bool {
	for _, e := range list {
		switch e.(type) {
		case []any, map[string]any:
			return false
		}
	}
	return true
}

// hasReference is true when a string anywhere in v holds a dollar sign, or a map holds a secret reference.
func hasReference(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.Contains(x, "$")
	case []any:
		return slices.ContainsFunc(x, hasReference)
	case map[string]any:
		if _, ok := x[fromSecretKey]; ok {
			return true
		}
		for _, e := range x {
			if hasReference(e) {
				return true
			}
		}
	}
	return false
}

// NoEntrypointReason is the text of the failing step for a plugin image whose entrypoint command is not known: the
// target has no form that runs the image entrypoint by itself.
func NoEntrypointReason(st *ir.Step) string {
	if st.Kind != ir.StepPlugin || st.Image == nil {
		return ""
	}
	return "the plugin image " + st.Image.Ref + " has no entrypoint command in the plugin table and the target cannot run an image entrypoint; set the entrypoint command of the image in the step"
}
