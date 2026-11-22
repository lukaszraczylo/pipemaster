package concourse_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
)

func FuzzParse(f *testing.F) {
	f.Add(oneJob(echoTask))
	f.Add(oneJob(echoTask + "  on_failure: {put: repo}\n  ensure: {do: [{task: a, config: {platform: linux, run: {path: ls}}}]}\n"))
	f.Add("resources:\n- {name: t, type: time, source: {interval: 1h}}\n- {name: r, type: git, source: {uri: u, branch: main, tag_filter: 'v*'}}\njobs:\n- {name: a, plan: [{get: t, trigger: true}, {get: r, trigger: true, passed: [b]}]}\n- {name: b, plan: [{in_parallel: [{get: r}]}, {try: {task: x, file: r/x.yml}}]}\n")
	f.Add("x: &a {platform: linux}\njobs:\n- name: j\n  serial_groups: [g]\n  plan:\n  - task: t\n    params: {A: ((s)), B: ((.:x))}\n    config: {<<: *a, run: {path: sh, args: [-c, 'x\\ny']}}\n    across: [{var: v, values: [1]}]\n")
	f.Add("jobs:\n- name: j\n  plan:\n  - attempts: 2\n    timeout: 1h\n    do: [{get: r, passed: ['*']}]\n")
	f.Fuzz(func(t *testing.T, src string) {
		p, _, err := concourse.New().Parse(repo(map[string]string{".concourse/p.yml": src}))
		if err != nil {
			return
		}
		if err := ir.Validate(p); err != nil {
			t.Fatalf("Parse returned an invalid project: %v", err)
		}
		if _, err := ir.Marshal(p); err != nil {
			t.Fatalf("Marshal: %v", err)
		}
	})
}
