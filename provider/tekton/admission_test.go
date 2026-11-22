package tekton_test

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var paramUseRE = regexp.MustCompile(`\$\(params\.([A-Za-z_][A-Za-z0-9_-]*)`)

// admission applies the Task and Pipeline checks that kubeconform cannot make, because the CRD schemas keep
// unknown fields: every $(params.X) in a Task resolves to a declared Task param, and every param default has
// the type of its param.
func admission(t *testing.T, files map[string]string) {
	t.Helper()
	for name, src := range files {
		if !strings.HasPrefix(name, ".tekton/") || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		for _, d := range docs(t, src) {
			switch d["kind"] {
			case "Task":
				checkTask(t, name, get(d, "metadata", "name"), d["spec"])
			case "Pipeline":
				checkDefaults(t, name, get(d, "spec", "params"))
				for _, pt := range list(get(d, "spec", "tasks")) {
					if spec := get(pt, "taskSpec"); spec != nil {
						checkTask(t, name, get(pt, "name"), spec)
					}
				}
			}
		}
	}
}

func checkTask(t *testing.T, file string, name, spec any) {
	t.Helper()
	declared := map[string]bool{}
	for _, p := range list(get(spec, "params")) {
		declared[fmt.Sprint(get(p, "name"))] = true
	}
	used := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, m := range paramUseRE.FindAllStringSubmatch(x, -1) {
				used[m[1]] = true
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case doc:
			for k, e := range x {
				if k != "params" {
					walk(e)
				}
			}
		}
	}
	walk(spec)
	var missing []string
	for n := range used {
		if !declared[n] {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%s: Task %v reads params that it does not declare: %v", file, name, missing)
	}
}

func checkDefaults(t *testing.T, file string, params any) {
	t.Helper()
	for _, p := range list(params) {
		def := get(p, "default")
		if def == nil {
			continue
		}
		typ, _ := get(p, "type").(string)
		var ok bool
		switch typ {
		case "array":
			_, ok = def.([]any)
		case "object":
			_, ok = def.(doc)
		default:
			_, ok = def.(string)
		}
		if !ok {
			t.Errorf("%s: param %v of type %q has default %#v", file, get(p, "name"), typ, def)
		}
	}
}
