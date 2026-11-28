package concourse_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
)

const inlineHead = "  - task: t\n    config:\n      platform: linux\n      image_resource: {type: registry-image, source: {repository: alpine}}\n"

func TestInlineTaskInputLinksTheCheckout(t *testing.T) {
	pl, rep := parseYAML(t, oneJob(inlineHead+"      inputs: [{name: repo}]\n      run: {path: sh, args: [-ec, 'cd repo && ls']}\n"))
	st := job(t, pl, "build").Steps[0]
	if !slices.Contains(st.Script, `[ -e repo ] || ln -s "$PWD" repo`) {
		t.Fatalf("script = %q, want a link of repo to the checkout", st.Script)
	}
	if got := entriesAt(rep, caps.Lost, "inputs"); len(got) != 0 {
		t.Errorf("a linked input is no loss: %+v", got)
	}
	if !hasEntry(rep, "link repo to the checkout") {
		t.Errorf("the link has no report entry: %+v", rep.Entries)
	}
}

func TestInlineTaskInputWithRunDir(t *testing.T) {
	pl, _ := parseYAML(t, oneJob(inlineHead+"      inputs: [{name: repo}]\n      run: {path: sh, args: [-ec, ls], dir: repo}\n"))
	st := job(t, pl, "build").Steps[0]
	if st.WorkDir != "" || !slices.Contains(st.Script, "cd 'repo'") {
		t.Fatalf("workdir = %q, script = %q: the cd must follow the link", st.WorkDir, st.Script)
	}
}

func TestInlineTaskInputUnknownArtifactStaysLost(t *testing.T) {
	_, rep := parseFiles(t, map[string]string{"ci.yml": oneJob(inlineHead + "      inputs: [{name: nothing}]\n      run: {path: sh, args: [-ec, ls]}\n")})
	if got := entriesAt(rep, caps.Lost, "input nothing"); len(got) != 1 {
		t.Errorf("an input with no source must stay reported lost: %+v", rep.Entries)
	}
}

func TestInlineTaskInputRoundTripsExactly(t *testing.T) {
	for name, run := range map[string]string{
		"shell form":   "run: {path: sh, args: [-ec, 'cd repo && ls']}",
		"command form": "run: {path: ls, args: [repo]}",
		"run dir":      "run: {path: sh, args: [-ec, ls], dir: repo}",
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := parseFiles(t, map[string]string{"ci.yml": oneJob(inlineHead + "      inputs: [{name: repo}]\n      " + run + "\n")})
			out, _ := emit(t, p)
			var body string
			for _, b := range out {
				body = b
			}
			if strings.Contains(body, "ln -s") {
				t.Errorf("the link lines must not return to the Concourse task:\n%s", body)
			}
			if !strings.Contains(body, strings.TrimPrefix(run, "run: ")[:0]+"inputs:") {
				t.Errorf("inputs must return:\n%s", body)
			}
		})
	}
}
