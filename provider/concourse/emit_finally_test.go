package concourse_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func finallyWhen(id, status string) *ir.Job {
	j := scriptJob(id, "echo "+id)
	if status != "" {
		j.If = callOf(status)
	}
	return j
}

func chainProject(finally ...*ir.Job) *ir.Project {
	build, deploy := scriptJob("build", "make"), scriptJob("deploy", "make deploy")
	deploy.Needs = []ir.Need{{JobID: "build"}}
	p := project(build, deploy)
	p.Pipelines[0].Finally = finally
	return p
}

// hookTaskNames lists the task names that a hook of the job holds, in order.
func hookTaskNames(t *testing.T, jd map[string]any, hook string) []string {
	t.Helper()
	h := dict(jd[hook])
	if h == nil {
		return nil
	}
	if h["task"] != nil {
		return []string{h["task"].(string)}
	}
	var out []string
	for _, s := range list(h["do"]) {
		out = append(out, dict(s)["task"].(string))
	}
	return out
}

func TestFinallyOnAChainRunsOnTheFirstFailure(t *testing.T) {
	files, rep := emit(t, chainProject(finallyWhen("recover", "failure"), finallyWhen("cleanup", "always"), finallyWhen("announce", "success")))
	d := pipelineDoc(t, files)
	build, deploy := jobDoc(t, d, "build"), jobDoc(t, d, "deploy")
	if got := hookTaskNames(t, build, "on_failure"); !slices.Equal(got, []string{"cleanup-run", "recover-run"}) {
		t.Fatalf("build on_failure = %v", got)
	}
	if build["ensure"] != nil || build["on_success"] != nil {
		t.Fatalf("build holds an ensure or on_success hook: %v", build)
	}
	if got := hookTaskNames(t, deploy, "on_failure"); !slices.Equal(got, []string{"recover-run"}) {
		t.Fatalf("deploy on_failure = %v", got)
	}
	if got := hookTaskNames(t, deploy, "ensure"); !slices.Equal(got, []string{"cleanup-run"}) {
		t.Fatalf("deploy ensure = %v", got)
	}
	if got := hookTaskNames(t, deploy, "on_success"); !slices.Equal(got, []string{"announce-run"}) {
		t.Fatalf("deploy on_success = %v", got)
	}
	if !hasFeatureEntry(rep, string(caps.PipelineFinally), "on_failure hook") {
		t.Fatalf("no emulated finally entry that names the on_failure hooks: %v", rep.Entries)
	}
}

func TestFinallyOnAFanOutIsLost(t *testing.T) {
	a, b, c, d := scriptJob("a", "true"), scriptJob("b", "true"), scriptJob("c", "true"), scriptJob("d", "true")
	b.Needs, c.Needs, d.Needs = []ir.Need{{JobID: "a"}}, []ir.Need{{JobID: "a"}}, []ir.Need{{JobID: "c"}}
	p := project(a, b, c, d)
	p.Pipelines[0].Finally = []*ir.Job{finallyWhen("cleanup", "always")}
	files, rep := emit(t, p)
	for _, j := range list(pipelineDoc(t, files)["jobs"]) {
		for _, h := range []string{"on_success", "on_failure", "ensure"} {
			if dict(j)[h] != nil {
				t.Fatalf("job %v holds a %s hook", dict(j)["name"], h)
			}
		}
	}
	lost := 0
	for _, e := range rep.Entries {
		if e.Feature == caps.PipelineFinally && e.Level == caps.Lost {
			lost++
		}
	}
	if lost != 2 {
		t.Fatalf("%d lost finally entries, want 2 (pipeline and job): %v", lost, rep.Entries)
	}
}

func TestFinallyOnAFanInIsLost(t *testing.T) {
	a, b, c := scriptJob("a", "true"), scriptJob("b", "true"), scriptJob("c", "true")
	c.Needs = []ir.Need{{JobID: "a"}, {JobID: "b"}}
	p := project(a, b, c)
	p.Pipelines[0].Finally = []*ir.Job{finallyWhen("cleanup", "always")}
	files, rep := emit(t, p)
	if dict(jobDoc(t, pipelineDoc(t, files), "c"))["ensure"] != nil || !hasFeatureEntry(rep, string(caps.PipelineFinally), "chain") {
		t.Fatalf("finally must be lost on a fan-in: %v", rep.Entries)
	}
}

func TestFinallyOnASingleJobIsUnchanged(t *testing.T) {
	p := project(scriptJob("build", "make"))
	p.Pipelines[0].Finally = []*ir.Job{finallyWhen("recover", "failure"), finallyWhen("cleanup", "always")}
	files, _ := emit(t, p)
	build := jobDoc(t, pipelineDoc(t, files), "build")
	if got := hookTaskNames(t, build, "on_failure"); !slices.Equal(got, []string{"recover-run"}) {
		t.Fatalf("on_failure = %v", got)
	}
	if got := hookTaskNames(t, build, "ensure"); !slices.Equal(got, []string{"cleanup-run"}) {
		t.Fatalf("ensure = %v", got)
	}
}

func TestFinallyOnAChainTagTwinsGetTheSamePlacement(t *testing.T) {
	p := chainProject(finallyWhen("cleanup", "always"))
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main"), {Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}}}}
	files, _ := emit(t, p)
	d := pipelineDoc(t, files)
	for _, name := range []string{"build", "build-tag"} {
		if got := hookTaskNames(t, jobDoc(t, d, name), "on_failure"); !slices.Equal(got, []string{"cleanup-run"}) {
			t.Fatalf("%s on_failure = %v", name, got)
		}
	}
	for _, name := range []string{"deploy", "deploy-tag"} {
		if got := hookTaskNames(t, jobDoc(t, d, name), "ensure"); !slices.Equal(got, []string{"cleanup-run"}) {
			t.Fatalf("%s ensure = %v", name, got)
		}
	}
}

func TestChainFinallyComesBackAsFinallyJobs(t *testing.T) {
	files, rep := emit(t, chainProject(finallyWhen("cleanup", "always")))
	got, prep := reparse(t, files)
	pl := got.Pipelines[0]
	if len(pl.Finally) != 1 {
		t.Fatalf("finally = %d, want 1: %v", len(pl.Finally), prep.Entries)
	}
	for _, j := range pl.Jobs {
		if len(j.Steps) != 1 {
			t.Fatalf("job %s has %d steps, want 1", j.ID, len(j.Steps))
		}
	}
	if hasFeatureEntry(prep, string(caps.PipelineFinally), "marker ignored") {
		t.Fatalf("unedited file reported an ignored marker: %v", prep.Entries)
	}
	_ = rep
}

func TestEditedFailureCopyOfAChainKeepsTheFile(t *testing.T) {
	files, _ := emit(t, chainProject(finallyWhen("cleanup", "always")))
	src := files[".concourse/ci.yml"]
	i := strings.LastIndex(src, "echo cleanup")
	if i < 0 {
		t.Fatalf("no cleanup script:\n%s", src)
	}
	src = src[:i] + "echo edited" + src[i+len("echo cleanup"):]
	got, rep := parseFiles(t, map[string]string{"ci.yml": src})
	if len(got.Pipelines[0].Finally) != 0 {
		t.Fatalf("finally restored from an edited file")
	}
	if !hasFeatureEntry(rep, string(caps.PipelineFinally), "marker ignored") {
		t.Fatalf("no marker ignored entry: %v", rep.Entries)
	}
}
