package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestDefaultShellOfBashSourcesRunsBashIfPresent(t *testing.T) {
	const bashOnly = `[[ a == a ]] && echo ok`
	for _, origin := range []string{"gha", "gitlab"} {
		t.Run(origin, func(t *testing.T) {
			p := project(scriptJob("build", bashOnly))
			p.Origin = ir.Origin{Provider: origin, Dialect: origin}
			files, rep := emitRaw(t, p)
			text := joinFiles(files)
			for _, l := range bashPreamble {
				if !strings.Contains(text, l) {
					t.Errorf("the files lack %q:\n%s", l, text)
				}
			}
			if e := entry(rep, "step.shell", "bash-only syntax fails under sh"); e == nil {
				t.Errorf("no caveat entry in %v", rep.Entries)
			}
			back, _ := reparse(t, files)
			st := back.Pipelines[0].Jobs[0].Steps[0]
			if st.Shell != "" || strings.Join(st.Script, "\n") != bashOnly {
				t.Errorf("step = shell %q script %q", st.Shell, st.Script)
			}
			again, _ := emitRaw(t, back)
			if joinFiles(again) != text {
				t.Errorf("a Tekton file with the preamble changes on re-emit:\n%s", joinFiles(again))
			}
		})
	}
}

func TestDefaultShellOfOtherSourcesKeepsPlainScript(t *testing.T) {
	p := project(scriptJob("build", "make"))
	p.Origin = ir.Origin{Provider: "concourse", Dialect: "concourse"}
	files, _ := emitRaw(t, p)
	if text := joinFiles(files); strings.Contains(text, "PIPEMASTER_BASH") {
		t.Errorf("a Concourse source must keep the plain script:\n%s", text)
	}
}

func TestExplicitShellKeepsItsShebang(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].Shell = "/bin/bash"
	files, _ := emitRaw(t, project(j))
	if text := joinFiles(files); strings.Contains(text, "PIPEMASTER_BASH") || !strings.Contains(text, "#!/bin/bash") {
		t.Errorf("an explicit shell must stay the shebang:\n%s", text)
	}
}
