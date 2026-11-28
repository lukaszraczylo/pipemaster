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

// runPluginSetting emits a slack plugin step with one literal setting, runs the script under sh with the
// entrypoint replaced by a probe, and returns the value the plugin sees. It fails when a side-effect file exists.
func runPluginSetting(t *testing.T, value string) string {
	t.Helper()
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
	if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
		t.Fatalf("a setting ran as a command\n%s", script)
	}
	got, err := os.ReadFile(out) // #nosec G304 -- path in the test temp dir
	if err != nil {
		t.Fatalf("%v\n%s", err, script)
	}
	return string(got)
}

// A literal setting must reach the plugin byte for byte: nothing in it runs or expands in the job shell.
// "in" is the setting text, "want" what the plugin sees; a ${{ 'x' }} literal expression yields x.
func TestGitLabPluginSettingsAreData(t *testing.T) {
	tests := map[string]struct{ in, want string }{
		"single quote":       {`it's`, `it's`},
		"double quote":       {`say "hi"`, `say "hi"`},
		"dollar name":        {`$HOME and ${HOME}`, `$HOME and ${HOME}`},
		"command":            {`$(touch PWNED)`, `$(touch PWNED)`},
		"backtick":           {"`touch PWNED`", "`touch PWNED`"},
		"backslash":          {`a\b\n\$`, `a\b\n\$`},
		"trailing backslash": {`a\`, `a\`},
		"double backslash":   {`a\\`, `a\\`},
		"double dollar":      {`$$ $? $1`, `$$ $? $1`},
		"trailing dollar":    {`cost$`, `cost$`},
		"quote then command": {`'; touch PWNED; echo '`, `'; touch PWNED; echo '`},
		"newline":            {"line1\nline2\n  indented\n", "line1\nline2\n  indented\n"},
		"newline command":    {"a\n$(touch PWNED)\nb", "a\n$(touch PWNED)\nb"},
		"unicode":            {"日本語 \U0001F600 é", "日本語 \U0001F600 é"},
		"empty":              {``, ``},
		"spaces":             {`  lead and trail  `, `  lead and trail  `},
		"glob":               {`*`, `*`},
		"semicolon":          {`a; b && c | d`, `a; b && c | d`},
		"expr command":       {`${{ '$(touch PWNED)' }}`, `$(touch PWNED)`},
		"expr embedded":      {`x ${{ 'a$(touch PWNED)' }} y'z`, `x a$(touch PWNED) y'z`},
		"expr home":          {`${{ 'a $HOME' }}`, `a $HOME`},
		"expr braces":        {`${{ '${HOME}' }}`, `${HOME}`},
		"expr backtick":      {"${{ '`touch PWNED`' }}", "`touch PWNED`"},
		"expr double quote":  {`${{ 'say "hi" \' }}`, `say "hi" \`},
		"expr backslash":     {`${{ 'a\b$' }}`, `a\b$`},
		"expr unicode":       {`${{ 'é日' }}`, `é日`},
		"expr then command":  {`${{ 'a' }}$(touch PWNED)`, `a$(touch PWNED)`},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := runPluginSetting(t, tt.in); got != tt.want {
				t.Fatalf("PLUGIN_TEXT = %q, want %q", got, tt.want)
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

// A secret that a setting reaches through a template keeps its external entry.
func TestGitLabPluginTemplateSecretVerdict(t *testing.T) {
	with := map[string]ir.Value{"password": lit("p=${{ secrets.docker_pw }}")}
	script, rep := gitlabPluginScript(t, pluginStep("plugins/slack", with))
	if !strings.Contains(script, `"$docker_pw"`) {
		t.Fatalf("the secret is not a variable reference:\n%s", script)
	}
	found := false
	for _, e := range rep.Entries {
		found = found || e.Level == caps.External && strings.Contains(e.Detail+e.Strategy, "docker_pw")
	}
	if !found {
		t.Errorf("no external entry names docker_pw: %+v", rep.Entries)
	}
}
