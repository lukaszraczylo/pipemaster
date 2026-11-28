package concourse_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

const bashOnly = `[[ a == a ]] && echo bash-ok`

// runsOf lists the run maps of every task in the document.
func runsOf(v any) []map[string]any {
	var out []map[string]any
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			out = append(out, runsOf(e)...)
		}
	case map[string]any:
		if r, ok := x["run"].(map[string]any); ok {
			out = append(out, r)
		}
		for _, e := range x {
			out = append(out, runsOf(e)...)
		}
	}
	return out
}

func argStrings(r map[string]any) []string {
	var out []string
	for _, a := range list(r["args"]) {
		s, _ := a.(string)
		out = append(out, s)
	}
	return out
}

func TestDefaultShellOfBashSourcesRunsBashIfPresent(t *testing.T) {
	for _, origin := range []string{"gha", "gitea", "forgejo", "gitlab"} {
		t.Run(origin, func(t *testing.T) {
			p := project(scriptJob("build", bashOnly))
			p.Origin = ir.Origin{Provider: origin, Dialect: origin}
			// A GitLab file with no trigger also runs on tags, which gives the job a tag twin; one push trigger keeps one task.
			p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main")}
			files, rep := emit(t, p)
			runs := runsOf(pipelineDoc(t, files))
			if len(runs) != 1 || runs[0]["path"] != "sh" {
				t.Fatalf("runs = %v", runs)
			}
			args := argStrings(runs[0])
			want := []string{"-c", provider.BashOrShScript, provider.BashOrShArgv0, bashOnly}
			if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("args = %q, want %q", args, want)
			}
			if !hasFeatureEntry(rep, "step.shell", "bash-only syntax fails under sh") {
				t.Errorf("no step.shell caveat in %v", rep.Entries)
			}
		})
	}
}

func TestDefaultShellOfOtherSourcesKeepsSh(t *testing.T) {
	for _, origin := range []string{"", "tekton", "drone"} {
		p := project(scriptJob("build", "make"))
		p.Origin = ir.Origin{Provider: origin, Dialect: origin}
		files, _ := emit(t, p)
		runs := runsOf(pipelineDoc(t, files))
		if got := argStrings(runs[0]); runs[0]["path"] != "sh" || strings.Join(got, " ") != "-ec make" {
			t.Errorf("origin %q: run = %v", origin, runs[0])
		}
	}
}

func TestExplicitShellKeepsItsHandling(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].Shell = "bash"
	files, _ := emit(t, project(j))
	runs := runsOf(pipelineDoc(t, files))
	if got := argStrings(runs[0]); runs[0]["path"] != "bash" || strings.Join(got, " ") != "-ec make" {
		t.Fatalf("run = %v", runs[0])
	}
}

func TestWrapperComesBackAsTheDefaultShell(t *testing.T) {
	p := project(scriptJob("build", bashOnly, `echo "it's $HOME"`))
	files, _ := emit(t, p)
	back, _ := reparse(t, files)
	st := back.Pipelines[0].Jobs[0].Steps[0]
	if st.Shell != "" || strings.Join(st.Script, "\n") != bashOnly+"\n"+`echo "it's $HOME"` {
		t.Fatalf("step = shell %q script %q", st.Shell, st.Script)
	}
	again, _ := emit(t, back)
	if strings.Join(argStrings(runsOf(pipelineDoc(t, again))[0]), "\x00") != strings.Join(argStrings(runsOf(pipelineDoc(t, files))[0]), "\x00") {
		t.Fatalf("a Concourse file with the wrapper loses it on re-emit:\n%v", again)
	}
}

func TestWrapperRunsBashWhenPresentElseSh(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	if _, err := exec.LookPath("bash"); err == nil {
		out, err := exec.Command(sh, "-c", provider.BashOrShScript, provider.BashOrShArgv0, bashOnly).CombinedOutput() // #nosec G204 -- fixed wrapper
		if err != nil || strings.TrimSpace(string(out)) != "bash-ok" {
			t.Errorf("with bash: %v %q", err, out)
		}
	}
	// A PATH with sh and no bash takes the sh branch.
	dir := t.TempDir()
	if err := os.Symlink(sh, filepath.Join(dir, "sh")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, "-c", provider.BashOrShScript, provider.BashOrShArgv0, "echo \"sh-$0\"") // #nosec G204 -- fixed wrapper
	cmd.Env = []string{"PATH=" + dir}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), "sh-") {
		t.Errorf("without bash: %v %q", err, out)
	}
}
