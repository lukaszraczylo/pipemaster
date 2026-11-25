package roundtrip_test

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// concourseTasks emits steps to Concourse and returns the plan entries of the first job, with the file text.
func concoursePlan(t *testing.T, steps ...*ir.Step) ([]map[string]any, string, map[string][]byte) {
	t.Helper()
	files, _, err := prov(t, "concourse").Emit(oneJob(steps...))
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range files {
		if !strings.HasSuffix(name, ".yml") {
			continue
		}
		var doc struct {
			Jobs []struct {
				Plan []map[string]any `yaml:"plan"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil || len(doc.Jobs) == 0 {
			continue
		}
		return doc.Jobs[0].Plan, string(b), files
	}
	t.Fatalf("no pipeline file in %v", names(files))
	return nil, "", nil
}

func taskNamed(t *testing.T, plan []map[string]any, name string) map[string]any {
	t.Helper()
	for _, s := range plan {
		if s["task"] == name {
			return s
		}
	}
	t.Fatalf("no task %s in %v", name, plan)
	return nil
}

// The first task of a job reads the source get through input_mapping, whatever kind of task it is, and the next
// task takes the workspace that the first one wrote.
func TestConcoursePluginTaskFirstGetsTheWorkspace(t *testing.T) {
	plugin := pluginStep("plugins/slack:1.4", map[string]ir.Value{"channel": lit("builds")})
	plugin.ID = "notify"
	plan, text, _ := concoursePlan(t, plugin, shStep("after", "echo after"))
	first, second := taskNamed(t, plan, "notify"), taskNamed(t, plan, "after")
	im, _ := first["input_mapping"].(map[string]any)
	if im["workspace"] == nil {
		t.Errorf("the plugin task has no input_mapping for the workspace:\n%s", text)
	}
	if second["input_mapping"] != nil {
		t.Errorf("the next task maps the workspace again, so it would read the source and not the plugin output:\n%s", text)
	}
	cfg, _ := first["config"].(map[string]any)
	if out, _ := cfg["outputs"].([]any); len(out) == 0 {
		t.Errorf("the plugin task writes no workspace output")
	}
	if out, err := linttool.FlyLint(t, "p.yml", []byte(text)); err != nil {
		t.Errorf("fly rejects the pipeline: %v\n%s", err, out)
	}
}

// A plugin that starts a Docker daemon needs a privileged task, which the task step can state.
func TestConcoursePrivilegedPluginTask(t *testing.T) {
	docker := pluginStep("plugins/docker:20", map[string]ir.Value{"repo": lit("x")})
	docker.ID = "build"
	plain := pluginStep("plugins/slack", map[string]ir.Value{"channel": lit("c")})
	plain.ID = "notify"
	plan, text, _ := concoursePlan(t, docker, plain)
	if taskNamed(t, plan, "build")["privileged"] != true {
		t.Errorf("the docker plugin task is not privileged:\n%s", text)
	}
	if taskNamed(t, plan, "notify")["privileged"] != nil {
		t.Errorf("a plain plugin task is privileged")
	}
	if strings.Contains(text, "cannot request") {
		t.Errorf("a wrong claim is in the file")
	}
	if out, err := linttool.FlyLint(t, "p.yml", []byte(text)); err != nil {
		t.Errorf("fly rejects the pipeline: %v\n%s", err, out)
	}
}

// The image reference keeps its tag and its digest exactly, whatever the registry is.
func TestConcoursePluginImageKeepsTagAndDigest(t *testing.T) {
	const d = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct{ ref, repo, tag, digest string }{
		{"plugins/slack", "plugins/slack", "", ""},
		{"plugins/slack:1.4", "plugins/slack", "1.4", ""},
		{"plugins/slack@" + d, "plugins/slack", "", d},
		{"plugins/slack:1.4@" + d, "plugins/slack", "1.4", d},
		{"docker.io/plugins/slack:1.2@" + d, "docker.io/plugins/slack", "1.2", d},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			st := pluginStep(tt.ref, map[string]ir.Value{"channel": lit("c")})
			st.ID = "notify"
			plan, text, _ := concoursePlan(t, st)
			cfg, _ := taskNamed(t, plan, "notify")["config"].(map[string]any)
			res, _ := cfg["image_resource"].(map[string]any)
			src, _ := res["source"].(map[string]any)
			got := [3]string{strOfAny(src["repository"]), strOfAny(src["tag"]), strOfAny(src["digest"])}
			if got != [3]string{tt.repo, tt.tag, tt.digest} {
				t.Errorf("repository, tag, digest = %q, want %q\n%s", got, [3]string{tt.repo, tt.tag, tt.digest}, text)
			}
		})
	}
}

func strOfAny(v any) string { s, _ := v.(string); return s }

// A plugin step with a condition names the real cause, not a missing entrypoint.
func TestConcoursePluginReasonNamesTheCause(t *testing.T) {
	st := pluginStep("plugins/slack", map[string]ir.Value{"channel": lit("c")})
	st.If = expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{"event"}}, R: expr.Lit{Kind: expr.LitStr, Str: "push"}}
	files, rep, err := prov(t, "concourse").Emit(oneJob(st))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range scriptCandidates(t, files) {
		if hasFailLoud(s) && strings.Contains(s, "no entrypoint command") {
			t.Errorf("the failing step blames the plugin table for a known image:\n%s", s)
		}
	}
	for _, e := range rep.Entries {
		if e.Feature == caps.StepAction && e.Level == caps.Emulated {
			t.Errorf("emulated entry for a step that fails: %+v", e)
		}
	}
}
