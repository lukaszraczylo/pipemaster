package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

func TestDefaultShellOfBashSourcesRunsBashIfPresent(t *testing.T) {
	const bashOnly = `[[ a == a ]] && echo ok`
	for _, origin := range []string{"gha", "gitlab"} {
		t.Run(origin, func(t *testing.T) {
			p := project(scriptJob("build", bashOnly))
			p.Origin = ir.Origin{Provider: origin, Dialect: origin}
			files, rep := emit(t, p)
			text := joinFiles(files)
			for _, want := range []string{"- -c\n", provider.BashOrShScript, "- " + provider.BashOrShArgv0 + "\n", bashOnly} {
				if !strings.Contains(text, want) {
					t.Errorf("the files lack %q:\n%s", want, text)
				}
			}
			if len(entries(rep, "bash-only syntax fails under sh")) == 0 {
				t.Errorf("no caveat entry in %v", rep.Entries)
			}
			back := roundTrip(t, files).Pipelines[0].Jobs[0].Steps[0]
			if back.Shell != "" || strings.Join(back.Script, "\n") != bashOnly {
				t.Errorf("step = shell %q script %q", back.Shell, back.Script)
			}
		})
	}
}

func TestDefaultShellOfOtherSourcesKeepsShEc(t *testing.T) {
	p := project(scriptJob("build", "make"))
	p.Origin = ir.Origin{Provider: "tekton", Dialect: "tekton"}
	files, _ := emit(t, p)
	if text := joinFiles(files); strings.Contains(text, provider.BashOrShScript) || !strings.Contains(text, "- -ec\n") {
		t.Errorf("a Tekton source must keep sh -ec:\n%s", text)
	}
}

func TestExplicitShellKeepsItsHandling(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].Shell = "bash"
	files, _ := emit(t, project(j))
	if text := joinFiles(files); strings.Contains(text, provider.BashOrShScript) || !strings.Contains(text, "- bash\n") {
		t.Errorf("an explicit shell must stay the command:\n%s", text)
	}
}
