package roundtrip_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func allText(files map[string][]byte) string {
	var sb strings.Builder
	for _, n := range names(files) {
		sb.Write(files[n])
	}
	return sb.String()
}

// A plugin that starts a Docker daemon runs privileged, and the file says so on Tekton and Argo.
func TestClusterPrivilegedPlugin(t *testing.T) {
	for _, to := range []string{"tekton", "argo"} {
		t.Run(to, func(t *testing.T) {
			files, rep, err := prov(t, to).Emit(oneJob(pluginStep("plugins/docker:20", map[string]ir.Value{"repo": lit("x")})))
			if err != nil {
				t.Fatal(err)
			}
			text := allText(files)
			if !strings.Contains(text, "securityContext:") || !strings.Contains(text, "privileged: true") {
				t.Errorf("no privileged security context:\n%s", text)
			}
			found := false
			for _, e := range rep.Entries {
				found = found || e.Level == caps.External && strings.Contains(e.Detail, "admit privileged pods")
			}
			if !found {
				t.Errorf("no external entry about the cluster policy: %+v", rep.Entries)
			}
			files, _, err = prov(t, to).Emit(oneJob(pluginStep("plugins/slack", map[string]ir.Value{"channel": lit("c")})))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(allText(files), "privileged") {
				t.Errorf("a plain plugin is privileged")
			}
		})
	}
}

// Argo substitutes {{ }} and has no documented escape, so a setting that holds one makes the step fail loudly.
func TestArgoPluginSettingWithATagFails(t *testing.T) {
	for name, value := range map[string]string{
		"template": "{{#success build.status}} ok {{build.number}} {{/success}}",
		"open":     "a {{ b",
		"close":    "a }} b",
	} {
		t.Run(name, func(t *testing.T) {
			files, rep, err := prov(t, "argo").Emit(oneJob(pluginStep("plugins/slack", map[string]ir.Value{"template": lit(value)})))
			if err != nil {
				t.Fatal(err)
			}
			text := allText(files)
			if strings.Contains(text, "PLUGIN_TEMPLATE") || strings.Contains(text, "build.number") && !strings.Contains(text, "pipemaster:arg") {
				t.Errorf("the value reaches the file as a tag:\n%s", text)
			}
			failing := false
			for _, s := range scriptCandidates(t, files) {
				failing = failing || hasFailLoud(s)
			}
			if !failing {
				t.Errorf("no failing step:\n%s", text)
			}
			for _, e := range rep.Entries {
				if e.Feature == caps.StepAction && e.Level == caps.Emulated {
					t.Errorf("emulated entry for a step that fails: %+v", e)
				}
			}
		})
	}
}
