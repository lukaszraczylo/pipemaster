package concourse_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const loadVarTask = `  - task: use
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: alpine}}
      params: {VERSION: ((.:ver))}
      run: {path: sh, args: [-ec, 'echo "$VERSION"']}
`

func entriesAt(rep *report.Report, level caps.Level, substr string) []report.Entry {
	var out []report.Entry
	for _, e := range rep.Entries {
		if e.Level == level && strings.Contains(e.Detail, substr) {
			out = append(out, e)
		}
	}
	return out
}

func TestLoadVarScriptStep(t *testing.T) {
	tests := []struct {
		name       string
		step       string
		wantKind   ir.StepKind
		wantScript string
		wantLost   string
	}{
		{"raw from the checkout", "  - {load_var: ver, file: repo/VERSION}\n", ir.StepScript, "cat 'VERSION' > '.pipemaster/vars/ver' || exit 1", ""},
		{"trim", "  - {load_var: ver, file: repo/VERSION, format: trim}\n", ir.StepScript, "_pm_v=$(cat 'VERSION')", ""},
		{"json by extension fails loud", "  - {load_var: ver, file: repo/v.json}\n", ir.StepPlugin, "", "format json"},
		{"yaml by extension fails loud", "  - {load_var: ver, file: repo/v.yml}\n", ir.StepPlugin, "", "format yaml"},
		{"artifact no target runs", "  - {load_var: ver, file: other/VERSION}\n", ir.StepPlugin, "", "artifact other"},
		{"path with ..", "  - {load_var: ver, file: repo/../x}\n", ir.StepPlugin, "", "no plain artifact path"},
		{"unknown key", "  - {load_var: ver, file: repo/VERSION, timeout: 1m}\n", ir.StepPlugin, "", "timeout"},
		{"unsafe name", "  - {load_var: 'a b', file: repo/VERSION}\n", ir.StepPlugin, "", "no plain file name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pl, rep := parseYAML(t, oneJob(tc.step))
			steps := pl.Jobs[0].Steps
			if len(steps) != 1 {
				t.Fatalf("want one step, got %d", len(steps))
			}
			st := steps[0]
			if st.Kind != tc.wantKind {
				t.Fatalf("kind %q, want %q", st.Kind, tc.wantKind)
			}
			if tc.wantKind == ir.StepScript {
				if !slices.ContainsFunc(st.Script, func(l string) bool { return strings.Contains(l, tc.wantScript) }) {
					t.Errorf("script %q lacks %q", st.Script, tc.wantScript)
				}
				if len(entriesAt(rep, caps.Lost, "load_var")) != 0 {
					t.Errorf("a script load_var is no loss: %+v", rep.Entries)
				}
				if len(entriesAt(rep, caps.Emulated, "load_var")) != 1 {
					t.Errorf("want one emulated load_var entry: %+v", rep.Entries)
				}
				return
			}
			if len(entriesAt(rep, caps.Lost, tc.wantLost)) == 0 {
				t.Errorf("want a lost entry naming %q: %+v", tc.wantLost, rep.Entries)
			}
		})
	}
}

func TestLoadVarReferenceRewrite(t *testing.T) {
	pl, rep := parseYAML(t, oneJob("  - {load_var: ver, file: repo/VERSION}\n"+loadVarTask))
	use := pl.Jobs[0].Steps[1]
	if len(use.Env) != 0 {
		t.Errorf("VERSION must not stay an env entry: %+v", use.Env)
	}
	want := []string{`VERSION="$(cat '.pipemaster/vars/ver')" || exit 1`, "export VERSION"}
	if len(use.Script) < 2 || use.Script[0] != want[0] || use.Script[1] != want[1] {
		t.Errorf("script %q, want first lines %q", use.Script, want)
	}
	if got := entriesAt(rep, caps.Lost, "VERSION"); len(got) != 0 {
		t.Errorf("a rewritten reference is no loss: %+v", got)
	}
	if got := entriesAt(rep, caps.Emulated, "params VERSION"); len(got) != 1 {
		t.Errorf("want one emulated entry for the params: %+v", rep.Entries)
	}
}

func TestLoadVarReferenceWithoutLoadVarStaysLost(t *testing.T) {
	_, rep := parseYAML(t, oneJob(loadVarTask))
	if got := entriesAt(rep, caps.Lost, "params VERSION"); len(got) != 1 {
		t.Errorf("a local var with no load_var has no value: %+v", rep.Entries)
	}
}

func TestLoadVarRoundTripAndEdit(t *testing.T) {
	src := oneJob("  - {load_var: ver, file: repo/VERSION}\n" + loadVarTask)
	pl, _ := parseYAML(t, src)
	proj := &ir.Project{Origin: ir.Origin{Provider: "concourse", Dialect: "concourse"}, Pipelines: []*ir.Pipeline{pl}}
	files, _ := emit(t, proj)
	text := files[".concourse/ci.yml"]
	for _, want := range []string{"load_var: ver", "file: repo/VERSION", "((.:ver))"} {
		if !strings.Contains(text, want) {
			t.Errorf("round trip lacks %q:\n%s", want, text)
		}
	}
	for _, bad := range []string{".pipemaster", "export VERSION"} {
		if strings.Contains(text, bad) {
			t.Errorf("round trip leaks %q:\n%s", bad, text)
		}
	}

	pl.Jobs[0].Steps[0].Script = []string{"echo changed"}
	files, _ = emit(t, &ir.Project{Origin: proj.Origin, Pipelines: []*ir.Pipeline{pl}})
	edited := files[".concourse/ci.yml"]
	if strings.Contains(edited, "load_var:") || !strings.Contains(edited, "echo changed") {
		t.Errorf("an edited load_var script must become a task:\n%s", edited)
	}
}

func TestLoadRefFailsWhenTheFileIsMissing(t *testing.T) {
	pl, _ := parseYAML(t, oneJob("  - {load_var: ver, file: repo/VERSION}\n"+loadVarTask))
	script := strings.Join(pl.Jobs[0].Steps[1].Script, "\n")
	cmd := exec.Command("sh", "-c", script) // #nosec G204 -- script comes from the parser under test
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a missing load_var file must fail the step, output %q", out)
	}
	if strings.Contains(string(out), "\n\n") || string(out) == "\n" {
		t.Errorf("the task body printed an empty value: %q", out)
	}
}
