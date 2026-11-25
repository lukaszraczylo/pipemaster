package roundtrip_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// pluginStep is a plugin step of the source with literal settings.
func pluginStep(image string, with map[string]ir.Value) *ir.Step {
	return &ir.Step{ID: "s", Kind: ir.StepPlugin, Image: &ir.Image{Ref: image}, With: with}
}

func lit(s string) ir.Value { return ir.Value{Lit: s} }

// gitlabPluginScript returns the script item that sets the settings and runs the entrypoint.
func gitlabPluginScript(t *testing.T, st *ir.Step) (string, *report.Report) {
	t.Helper()
	files, rep, err := prov(t, "gitlab").Emit(oneJob(st))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range scriptCandidates(t, files) {
		if strings.Contains(s, "export PLUGIN_") {
			return s, rep
		}
	}
	return "", rep
}

// A literal setting must reach the plugin byte for byte: nothing in it runs or expands in the job shell.
func TestGitLabPluginSettingsAreData(t *testing.T) {
	hostile := map[string]string{
		"single quote":       `it's`,
		"double quote":       `say "hi"`,
		"dollar name":        `$HOME and ${HOME}`,
		"command":            `$(touch PWNED)`,
		"backtick":           "`touch PWNED`",
		"backslash":          `a\b\n\$`,
		"double dollar":      `$$ $? $1`,
		"quote then command": `'; touch PWNED; echo '`,
		"empty":              ``,
		"spaces":             `  lead and trail  `,
		"glob":               `*`,
		"semicolon":          `a; b && c | d`,
	}
	for name, value := range hostile {
		t.Run(name, func(t *testing.T) {
			script, _ := gitlabPluginScript(t, pluginStep("plugins/slack", map[string]ir.Value{"text": lit(value)}))
			if script == "" {
				t.Fatal("no script with the settings")
			}
			if !strings.Contains(script, "'/bin/drone-slack'") {
				t.Fatalf("the entrypoint is missing:\n%s", script)
			}
			dir := t.TempDir()
			out := filepath.Join(dir, "got")
			probe := strings.Replace(script, "'/bin/drone-slack'", `printf '%s' "$PLUGIN_TEXT" > `+out, 1)
			_, stderr, code := runScriptIn(t, dir, probe)
			if code != 0 {
				t.Fatalf("exit %d, stderr %q\n%s", code, stderr, probe)
			}
			got, err := os.ReadFile(out) // #nosec G304 -- path in the test temp dir
			if err != nil || string(got) != value {
				t.Fatalf("PLUGIN_TEXT = %q (%v), want %q\n%s", got, err, value, script)
			}
			if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
				t.Fatalf("a setting ran as a command\n%s", script)
			}
		})
	}
}

// A setting that GitLab cannot carry stops the step: the plugin must not run with part of its settings.
func TestGitLabPluginWithALostSettingFails(t *testing.T) {
	for name, with := range map[string]map[string]ir.Value{
		"carriage return": {"multi": lit("a\rb"), "repo": lit("x")},
		"bad name":        {"a b": lit("1"), "repo": lit("x")},
		"nul only":        {"multi": lit("line1\x00line2")},
	} {
		t.Run(name, func(t *testing.T) {
			files, rep, err := prov(t, "gitlab").Emit(oneJob(pluginStep("plugins/docker:20", with)))
			if err != nil {
				t.Fatal(err)
			}
			all := ""
			for _, b := range files {
				all += string(b)
			}
			if strings.Contains(all, "export PLUGIN_") || strings.Contains(all, "drone-docker") {
				t.Errorf("the plugin runs with a setting dropped:\n%s", all)
			}
			failing := false
			for _, s := range scriptCandidates(t, files) {
				failing = failing || hasFailLoud(s) && strings.Contains(s, "no safe GitLab spelling")
			}
			if !failing {
				t.Errorf("no failing step that names the cause:\n%s", all)
			}
			for _, e := range rep.Entries {
				if e.Feature == caps.StepAction && e.Level == caps.Emulated {
					t.Errorf("emulated entry for a step that fails: %+v", e)
				}
			}
		})
	}
}

// A secret setting keeps its external verdict, and the job never runs with the variable unnamed.
func TestGitLabPluginSecretVerdict(t *testing.T) {
	with := map[string]ir.Value{"password": {Kind: ir.ValueSecret, Secret: "docker_pw"}, "repo": lit("x")}
	script, rep := gitlabPluginScript(t, pluginStep("plugins/slack", with))
	if !strings.Contains(script, `export PLUGIN_PASSWORD="$docker_pw"`) {
		t.Fatalf("the secret is not a variable reference:\n%s", script)
	}
	found := false
	for _, e := range rep.Entries {
		found = found || e.Level == caps.External && strings.Contains(e.Detail+e.Strategy, "docker_pw")
	}
	if !found {
		t.Errorf("no external entry names docker_pw: %+v", rep.Entries)
	}
	if strings.Contains(script, "s3cr3t") {
		t.Errorf("a secret value is written")
	}
}

// A plugin image that holds no shell cannot run a GitLab script.
func TestGitLabPluginWithoutShellFails(t *testing.T) {
	for _, image := range []string{"plugins/s3", "woodpeckerci/plugin-s3:2", "woodpeckerci/plugin-github-release"} {
		t.Run(image, func(t *testing.T) {
			files, rep, err := prov(t, "gitlab").Emit(oneJob(pluginStep(image, map[string]ir.Value{"bucket": lit("b")})))
			if err != nil {
				t.Fatal(err)
			}
			failing := false
			for _, s := range scriptCandidates(t, files) {
				failing = failing || hasFailLoud(s) && strings.Contains(s, "holds no shell") && strings.Contains(s, image)
			}
			if !failing {
				t.Errorf("no failing step that says the image holds no shell")
			}
			for _, e := range rep.Entries {
				if e.Feature == caps.StepAction && e.Level == caps.Emulated {
					t.Errorf("emulated entry for a step that fails: %+v", e)
				}
			}
		})
	}
}

// A plugin that starts a Docker daemon needs a privileged runner, which only the runner config can give.
func TestGitLabPrivilegedPluginIsExternal(t *testing.T) {
	_, rep := gitlabPluginScript(t, pluginStep("plugins/docker:20", map[string]ir.Value{"repo": lit("x")}))
	found := false
	for _, e := range rep.Entries {
		found = found || e.Level == caps.External && strings.Contains(e.Detail, "privileged = true")
	}
	if !found {
		t.Errorf("no external entry names privileged = true: %+v", rep.Entries)
	}
}
