package concourse_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

func runnerEntry(rep *report.Report, level caps.Level) *report.Entry {
	for i := range rep.Entries {
		if e := &rep.Entries[i]; e.Feature == caps.JobRunner && e.Level == level {
			return e
		}
	}
	return nil
}

func TestHostedRunnerLabelsBecomeNoWorkerTag(t *testing.T) {
	for _, tc := range []struct {
		label string
		text  string
		level caps.Level
	}{
		{"ubuntu-latest", "any Linux worker", caps.External},
		{"ubuntu-22.04", "any Linux worker", caps.External},
		{"macos-14", "macOS", caps.Lost},
		{"windows-2022", "Windows", caps.Lost},
	} {
		t.Run(tc.label, func(t *testing.T) {
			j := scriptJob("build", "true")
			j.Runner.Labels = []string{tc.label}
			p := project(j)
			p.Origin.Provider = "gha"
			files, rep := emit(t, p)
			for name, src := range files {
				if strings.Contains(src, "tags:") {
					t.Fatalf("%s carries a worker tag:\n%s", name, src)
				}
			}
			e := runnerEntry(rep, tc.level)
			if e == nil || !strings.Contains(e.Detail, tc.text) {
				t.Fatalf("no %v entry naming %q: %+v", tc.level, tc.text, rep.Entries)
			}
		})
	}
}

func TestSelfHostedLabelsStayWorkerTags(t *testing.T) {
	j := scriptJob("build", "true")
	j.Runner = ir.Runner{Labels: []string{"self-hosted", "gpu"}}
	p := project(j)
	p.Origin.Provider = "gha"
	files, rep := emit(t, p)
	task := dict(list(jobDoc(t, pipelineDoc(t, files), "build")["plan"])[1])
	if tags := list(task["tags"]); !slices.Equal(tags, []any{"gpu", "self-hosted"}) {
		t.Fatalf("tags = %v", task["tags"])
	}
	if e := runnerEntry(rep, caps.External); e == nil || !strings.Contains(e.Detail, "gpu, self-hosted") {
		t.Fatalf("no external entry naming the worker tags: %+v", rep.Entries)
	}
}

func TestHostedLabelOfAnotherDialectStaysATag(t *testing.T) {
	j := scriptJob("build", "true")
	j.Runner.Labels = []string{"ubuntu-latest"}
	p := project(j)
	p.Origin.Provider = "gitlab"
	files, _ := emit(t, p)
	task := dict(list(jobDoc(t, pipelineDoc(t, files), "build")["plan"])[1])
	if tags := list(task["tags"]); len(tags) != 1 || tags[0] != "ubuntu-latest" {
		t.Fatalf("tags = %v", task["tags"])
	}
}
