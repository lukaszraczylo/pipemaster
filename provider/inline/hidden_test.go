package inline_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/inline"
)

func TestFlattenUnsetsHiddenParentVariablesInChildScripts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger map[string]any
		script  string
		want    string
	}{
		{"hidden", map[string]any{"include": "ci/child.yml", "forward": map[string]any{"yaml_variables": false}}, "echo s2", "unset -v PARENT\necho s2"},
		{"hidden after shebang", map[string]any{"include": "ci/child.yml", "forward": map[string]any{"yaml_variables": false}}, "#!/bin/sh\necho s2", "#!/bin/sh\nunset -v PARENT\necho s2"},
		{"forwarded", map[string]any{"include": "ci/child.yml", "forward": map[string]any{"pipeline_variables": true}}, "echo s2", "echo s2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := gitlabProject(tc.trigger)
			src.Pipelines[0].Env = []ir.Var{{Name: "PARENT", Value: ir.Value{Lit: "x"}}}
			find(t, src, "call").Child.Pipeline.Jobs[1].Steps[0].Script = []string{tc.script}
			flat, _ := inline.Flatten(src, "drone")
			got := strings.Join(find(t, flat, "call-s2").Steps[0].Script, "\n")
			if got != tc.want {
				t.Errorf("script = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFlattenUnsetFormPerShell(t *testing.T) {
	hide := map[string]any{"include": "ci/child.yml", "forward": map[string]any{"yaml_variables": false}}
	for _, tc := range []struct {
		name, shell, script, want string
		entry                     bool
	}{
		{"default shell", "", "echo s2", "unset -v PARENT\necho s2", false},
		{"bash", "bash", "echo s2", "unset -v PARENT\necho s2", false},
		{"sh path", "/bin/sh", "echo s2", "unset -v PARENT\necho s2", false},
		{"pwsh", "pwsh", "Write-Host s2", "Remove-Item Env:PARENT -ErrorAction Ignore\nWrite-Host s2", false},
		{"powershell", "powershell", "Write-Host s2", "Remove-Item Env:PARENT -ErrorAction Ignore\nWrite-Host s2", false},
		{"cmd", "cmd", "echo s2", "set PARENT=\necho s2", false},
		{"python", "python", "print('s2')", "print('s2')", true},
		{"node", "node", "console.log('s2')", "console.log('s2')", true},
		{"custom template", "perl {0}", "print 1", "print 1", true},
		{"python shebang", "", "#!/usr/bin/env python3\nprint('s2')", "#!/usr/bin/env python3\nprint('s2')", true},
		{"env bash shebang", "", "#!/usr/bin/env bash\necho s2", "#!/usr/bin/env bash\nunset -v PARENT\necho s2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := gitlabProject(hide)
			src.Pipelines[0].Env = []ir.Var{{Name: "PARENT", Value: ir.Value{Lit: "x"}}}
			st := find(t, src, "call").Child.Pipeline.Jobs[1].Steps[0]
			st.Shell, st.Script = tc.shell, []string{tc.script}
			flat, res := inline.Flatten(src, "drone")
			if got := strings.Join(find(t, flat, "call-s2").Steps[0].Script, "\n"); got != tc.want {
				t.Errorf("script = %q, want %q", got, tc.want)
			}
			entry := false
			for _, e := range res.Entries {
				if e.Feature == caps.StepShell && strings.Contains(e.Detail, "PARENT") && strings.HasSuffix(e.IRPath, "/job/call-s2/step/"+st.ID) {
					entry = true
				}
			}
			if entry != tc.entry {
				t.Errorf("report entry naming PARENT = %v, want %v", entry, tc.entry)
			}
		})
	}
}

func TestFlattenReportsVariablesSetByHandReachingChildScripts(t *testing.T) {
	for _, tc := range []struct {
		fwd  map[string]any
		name string
		want bool
	}{
		{map[string]any{"yaml_variables": false}, "not forwarded", true},
		{map[string]any{"pipeline_variables": true}, "forwarded", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := gitlabProject(map[string]any{"include": "ci/child.yml", "forward": tc.fwd})
			_, res := inline.Flatten(src, "drone")
			got := false
			for _, e := range res.Entries {
				if e.Strategy == inline.EntryStrategy && strings.Contains(e.Detail, "sets by hand") && strings.Contains(e.Detail, "reaches the scripts of the child jobs, because they run in the same pipeline") {
					got = true
				}
			}
			if got != tc.want {
				t.Errorf("caveat present = %v, want %v", got, tc.want)
			}
		})
	}
}
