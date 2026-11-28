//go:build behave

package behave

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// githubPayload returns the GitHub webhook event name and body that simulate the workspace event. The generated
// Tekton EventListeners and Argo sensors read this shape.
func githubPayload(w *Workspace) (string, map[string]any) {
	repo := map[string]any{"full_name": repoFullName, "default_branch": mainBranch}
	body := map[string]any{"repository": repo}
	if w.Ev.Kind == kindPR {
		body["action"] = "opened"
		body["number"] = 1
		body["pull_request"] = map[string]any{
			"number": 1,
			"head":   map[string]any{"ref": w.Ev.Branch, "sha": w.SHA, "repo": repo},
			"base":   map[string]any{"ref": w.Ev.Target, "repo": repo},
		}
		return kindPR, body
	}
	body["ref"] = w.Ev.Ref()
	body["after"] = w.SHA
	return kindPush, body
}

// canonicalCtx is the value set for ctx-* parameters when a converted pipeline ships no trigger resources. The
// event names follow Woodpecker and Drone.
func canonicalCtx(w *Workspace) map[string]string {
	branch, event := w.Ev.Branch, w.Ev.Kind
	if w.Ev.Kind == kindTag {
		branch = ""
	}
	if w.Ev.Kind == kindPR {
		branch = w.Ev.Target
	}
	refType := "branch"
	if w.Ev.Kind == kindTag {
		refType = "tag"
	}
	ctx := map[string]string{
		"ctx-ref":      w.Ev.Ref(),
		"ctx-ref-name": strings.TrimPrefix(strings.TrimPrefix(w.Ev.Ref(), "refs/heads/"), "refs/tags/"),
		"ctx-ref-type": refType,
		"ctx-branch":   branch,
		"ctx-tag":      w.Ev.Tag,
		"ctx-sha":      w.SHA,
		"ctx-event":    event,
		"ctx-repo":     repoFullName,
	}
	if w.Ev.Kind == kindPR {
		ctx["ctx-pr-number"] = "1"
		ctx["ctx-pr-head-ref"] = w.Ev.Branch
		ctx["ctx-pr-base-ref"] = w.Ev.Target
	}
	return ctx
}

// readDocs reads every YAML document of a file.
func readDocs(path string) ([]map[string]any, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a file of the converted output
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var out []map[string]any
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if d != nil {
			out = append(out, d)
		}
	}
}

// readDir reads every YAML document under dir, recursively, in name order.
func readDir(dir string) ([]map[string]any, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []map[string]any
	for _, f := range files {
		docs, err := readDocs(f)
		if err != nil {
			return nil, err
		}
		out = append(out, docs...)
	}
	return out, nil
}

func str(m any, keys ...string) string {
	v := dig(m, keys...)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func dig(m any, keys ...string) any {
	cur := m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func list(v any) []map[string]any {
	var out []map[string]any
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func kind(d map[string]any) string { return str(d, "kind") }

func name(d map[string]any) string { return str(d, "metadata", "name") }

var tektonRef = regexp.MustCompile(`\$\(([A-Za-z0-9_.\-]+)\)`)

// tektonTrigger is one EventListener trigger that fired for the event.
type tektonTrigger struct {
	Params   map[string]string
	Template string
}

// selectTektonTriggers evaluates the EventListener of the output against the payload: the GitHub event type, the CEL
// filter, the overlays and the binding params. An expression outside the supported subset returns an error.
func selectTektonTriggers(docs []map[string]any, evType string, body map[string]any) ([]tektonTrigger, error) {
	bindings := map[string]map[string]any{}
	for _, d := range docs {
		if kind(d) == "TriggerBinding" {
			bindings[name(d)] = d
		}
	}
	var out []tektonTrigger
	for _, d := range docs {
		if kind(d) != "EventListener" {
			continue
		}
		for _, t := range list(dig(d, "spec", "triggers")) {
			ok, ext, err := evalInterceptors(list(t["interceptors"]), evType, body)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			params := map[string]string{}
			for _, b := range list(t["bindings"]) {
				bd, found := bindings[str(b, "ref")]
				if !found {
					return nil, fmt.Errorf("trigger %s: binding %q is not in the output", str(t, "name"), str(b, "ref"))
				}
				for _, p := range list(dig(bd, "spec", "params")) {
					v, err := bindingValue(str(p, "value"), body, ext)
					if err != nil {
						return nil, err
					}
					params[str(p, "name")] = v
				}
			}
			out = append(out, tektonTrigger{Template: str(t, "template", "ref"), Params: params})
		}
	}
	return out, nil
}

func evalInterceptors(ints []map[string]any, evType string, body map[string]any) (bool, map[string]string, error) {
	ext := map[string]string{}
	for _, in := range ints {
		switch str(in, "ref", "name") {
		case "github":
			for _, p := range list(in["params"]) {
				if str(p, "name") != "eventTypes" {
					continue
				}
				match := false
				if types, ok := p["value"].([]any); ok {
					for _, x := range types {
						match = match || fmt.Sprint(x) == evType
					}
				}
				if !match {
					return false, nil, nil
				}
			}
		case "cel":
			for _, p := range list(in["params"]) {
				switch str(p, "name") {
				case "filter":
					v, err := celEval(str(p, "value"), body)
					if err != nil {
						return false, nil, err
					}
					if !truthy(v) {
						return false, nil, nil
					}
				case "overlays":
					over, _ := p["value"].([]any)
					for _, o := range over {
						om, _ := o.(map[string]any)
						v, err := celEval(str(om, "expression"), body)
						if err != nil {
							return false, nil, err
						}
						ext[str(om, "key")] = fmt.Sprint(v)
					}
				}
			}
		default:
			return false, nil, fmt.Errorf("interceptor %q is not simulated by the harness", str(in, "ref", "name"))
		}
	}
	return true, ext, nil
}

func bindingValue(v string, body map[string]any, ext map[string]string) (string, error) {
	m := tektonRef.FindStringSubmatch(v)
	if m == nil || m[0] != v {
		return v, nil
	}
	switch {
	case strings.HasPrefix(m[1], "extensions."):
		return ext[strings.TrimPrefix(m[1], "extensions.")], nil
	case strings.HasPrefix(m[1], "body."):
		x := bodyPath(body, m[1])
		if x == nil {
			return "", nil
		}
		return fmt.Sprint(x), nil
	}
	return "", fmt.Errorf("binding value %q is not simulated by the harness", v)
}

// substituteTemplate replaces $(tt.params.NAME) in text with quoted values, falling back to the template defaults.
func substituteTemplate(text string, defaults []map[string]any, got map[string]string) string {
	vals := map[string]string{}
	for _, p := range defaults {
		vals[str(p, "name")] = str(p, "default")
	}
	for k, v := range got {
		vals[k] = v
	}
	return tektonRef.ReplaceAllStringFunc(text, func(tok string) string {
		n := strings.TrimPrefix(strings.Trim(tok, "$()"), "tt.params.")
		if v, ok := vals[n]; ok && strings.HasPrefix(strings.Trim(tok, "$()"), "tt.params.") {
			return fmt.Sprintf("%q", v)
		}
		return tok
	})
}

// ---- Argo sensor

var argoTrim = regexp.MustCompile(`^\{\{\s*trimPrefix\s+"([^"]*)"\s+\.Input\.(body[\w.]*)\s*\}\}$`)

// argoWorkflows evaluates the Sensor of the output and returns one Workflow object per trigger that fires.
func argoWorkflows(docs []map[string]any, evType string, body map[string]any) ([]map[string]any, error) {
	var out []map[string]any
	for _, d := range docs {
		if kind(d) != "Sensor" {
			continue
		}
		deps := map[string]bool{}
		for _, dep := range list(dig(d, "spec", "dependencies")) {
			ok, err := argoDepFires(dep, evType, body)
			if err != nil {
				return nil, err
			}
			deps[str(dep, "name")] = ok
		}
		for _, t := range list(dig(d, "spec", "triggers")) {
			tpl := dig(t, "template")
			if !argoConditions(str(tpl.(map[string]any), "conditions"), deps) {
				continue
			}
			res, ok := dig(tpl, "k8s", "source", "resource").(map[string]any)
			if !ok {
				return nil, fmt.Errorf("trigger %s has no resource", str(tpl, "name"))
			}
			wf, err := deepCopy(res)
			if err != nil {
				return nil, err
			}
			for _, p := range list(dig(tpl, "k8s", "parameters")) {
				v, err := argoParam(dig(p, "src"), body)
				if err != nil {
					return nil, err
				}
				if err := setPath(wf, strings.Split(str(p, "dest"), "."), v); err != nil {
					return nil, err
				}
			}
			out = append(out, wf)
		}
	}
	return out, nil
}

func argoConditions(cond string, deps map[string]bool) bool {
	if strings.TrimSpace(cond) == "" {
		for _, v := range deps {
			if v {
				return true
			}
		}
		return false
	}
	for _, or := range strings.Split(cond, "||") {
		all := true
		for _, and := range strings.Split(or, "&&") {
			all = all && deps[strings.Trim(strings.TrimSpace(and), "()")]
		}
		if all {
			return true
		}
	}
	return false
}

func argoDepFires(dep map[string]any, evType string, body map[string]any) (bool, error) {
	if str(dep, "eventName") != evType {
		return false, nil
	}
	for _, f := range list(dig(dep, "filters", "data")) {
		got := fmt.Sprint(bodyPath(body, str(f, "path")))
		match := false
		vals, _ := f["value"].([]any)
		for _, v := range vals {
			re, err := regexp.Compile("^(?:" + fmt.Sprint(v) + ")$")
			if err != nil {
				return false, fmt.Errorf("data filter %v: %w", v, err)
			}
			match = match || re.MatchString(got)
		}
		if !match {
			return false, nil
		}
	}
	for _, f := range list(dig(dep, "filters", "exprs")) {
		fields := map[string]string{}
		for _, fl := range list(f["fields"]) {
			fields[str(fl, "name")] = fmt.Sprint(bodyPath(body, str(fl, "path")))
		}
		parts := strings.Fields(str(f, "expr"))
		if len(parts) != 3 || (parts[1] != "==" && parts[1] != "!=") {
			return false, fmt.Errorf("sensor expression %q is not simulated by the harness", str(f, "expr"))
		}
		if (fields[parts[0]] == fields[parts[2]]) != (parts[1] == "==") {
			return false, nil
		}
	}
	return true, nil
}

func argoParam(src any, body map[string]any) (string, error) {
	m, ok := src.(map[string]any)
	if !ok {
		return "", fmt.Errorf("sensor parameter has no source")
	}
	if v := str(m, "value"); v != "" {
		return v, nil
	}
	if k := str(m, "dataKey"); k != "" {
		return fmt.Sprint(bodyPath(body, k)), nil
	}
	if t := str(m, "dataTemplate"); t != "" {
		g := argoTrim.FindStringSubmatch(t)
		if g == nil {
			return "", fmt.Errorf("data template %q is not simulated by the harness", t)
		}
		return strings.TrimPrefix(fmt.Sprint(bodyPath(body, g[2])), g[1]), nil
	}
	return "", nil
}

func deepCopy(m map[string]any) (map[string]any, error) {
	raw, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, yaml.Unmarshal(raw, &out)
}

// setPath sets a dotted path such as spec.arguments.parameters.0.value; numeric parts index lists.
func setPath(root map[string]any, parts []string, v string) error {
	var cur any = root
	for i, p := range parts {
		last := i == len(parts)-1
		switch c := cur.(type) {
		case map[string]any:
			if last {
				c[p] = v
				return nil
			}
			cur = c[p]
		case []any:
			var idx int
			if _, err := fmt.Sscanf(p, "%d", &idx); err != nil || idx >= len(c) {
				return fmt.Errorf("path %s: bad index %q", strings.Join(parts, "."), p)
			}
			if last {
				c[idx] = v
				return nil
			}
			cur = c[idx]
		default:
			return fmt.Errorf("path %s does not exist", strings.Join(parts, "."))
		}
	}
	return nil
}
