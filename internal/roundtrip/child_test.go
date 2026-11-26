package roundtrip_test

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

const childSrcRoot = `stages: [one]
call:
  stage: one
  rules:
    - if: '$CI_COMMIT_BRANCH == "main"'
      when: manual
  trigger:
    include: ci/child.yml
    strategy: depend
`

const childSrcChild = `stages: [s1, s2]
a:
  stage: s1
  script: [echo CHILD-A]
b:
  stage: s2
  needs: [a]
  script: [echo CHILD-B]
`

// childJobs returns the jobs of p that run a script of the child.
func childJobs(p *ir.Project) []*ir.Job {
	var out []*ir.Job
	for _, pl := range p.Pipelines {
		for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
			for _, st := range j.Steps {
				if strings.Contains(strings.Join(st.Script, "\n"), "CHILD-") {
					out = append(out, j)
					break
				}
			}
		}
	}
	return out
}

// childVariants are the sources whose child jobs the converter must gate. want is a text that the condition of each
// child job holds, or empty for a child that GitLab never starts from a parent.
var childVariants = []struct {
	name, root, child, want string
	manual                  bool
}{
	{"call rule", childSrcRoot, childSrcChild, "main", true},
	{"child branch rule", "call:\n  trigger:\n    include: ci/child.yml\n    strategy: depend\n",
		"workflow:\n  rules:\n    - if: '$CI_COMMIT_BRANCH == \"main\"'\n" + childSrcChild, "main", false},
	{"child push source rule", "call:\n  trigger:\n    include: ci/child.yml\n    strategy: depend\n",
		"workflow:\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"push\"'\n" + childSrcChild, "", false},
}

// TestInlineGatesEveryChildJobOnEveryTarget emits each variant into every target, reads the files back, and checks
// that every job that runs child work keeps the gate of the source.
func TestInlineGatesEveryChildJobOnEveryTarget(t *testing.T) {
	for _, v := range childVariants {
		src, _, err := prov(t, provider.GitLab).Parse(fstest.MapFS{
			".gitlab-ci.yml": {Data: []byte(v.root)}, "ci/child.yml": {Data: []byte(v.child)},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range provider.IDs() {
			if id == provider.GitLab {
				continue
			}
			t.Run(v.name+"/"+id, func(t *testing.T) {
				to := prov(t, id)
				files, rep, err := to.Emit(src)
				if err != nil {
					t.Fatal(err)
				}
				back, _, err := to.Parse(toFS(files))
				if err != nil {
					t.Fatal(err)
				}
				jobs := childJobs(back)
				if len(jobs) != 2 {
					t.Fatalf("%d jobs run child work, want 2:\n%v", len(jobs), files)
				}
				for _, j := range jobs {
					lost := slices.ContainsFunc(rep.Entries, func(e report.Entry) bool {
						return e.Level == caps.Lost && e.Feature == caps.JobManual && strings.HasSuffix(e.IRPath, "/job/"+j.ID)
					})
					// A target with no manual gate says so in a lost entry for the job, as it does for any manual job.
					if v.manual && !j.Manual && !lost {
						t.Errorf("job %s lost the manual start of the call and no lost entry says so", j.ID)
					}
					cond := expr.String(expr.FoldConst(j.If))
					if v.want == "" {
						// Tekton writes a never-true when expression that its parser reads back as opaque.
						if j.If == nil || (cond != "false" && !strings.Contains(cond, "pipemaster-disabled")) {
							t.Errorf("job %s has condition %q, want false: GitLab never starts this child from a parent", j.ID, cond)
						}
						continue
					}
					if j.If == nil || !strings.Contains(cond, v.want) {
						t.Errorf("job %s has condition %q, want a test for %q", j.ID, cond, v.want)
					}
				}
			})
		}
	}
}

// childSources are a GitLab and a GitHub Actions source that call a same-repository child.
var childSources = map[string]fstest.MapFS{
	provider.GitLab: {
		".gitlab-ci.yml": {Data: []byte("stages: [one, two]\nsetup:\n  stage: one\n  script: [echo setup]\ncall:\n  stage: two\n  trigger:\n    include: ci/child.yml\n    strategy: depend\n")},
		"ci/child.yml":   {Data: []byte(childSrcChild)},
	},
	provider.GHA: {
		".github/workflows/ci.yml": {Data: []byte("on: push\njobs:\n  setup:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo setup\n  call:\n    needs: setup\n    uses: ./.github/workflows/x.yml\n")},
		".github/workflows/x.yml":  {Data: []byte("on:\n  workflow_call:\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo CHILD-A\n  b:\n    needs: a\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo CHILD-B\n")},
	},
}

// TestCallRoundTripsExactly sends each source through every target and back with and without embedded metadata.
func TestCallRoundTripsExactly(t *testing.T) {
	for source, fsys := range childSources {
		for _, to := range targets(source) {
			for _, embed := range []bool{false, true} {
				t.Run(source+"/"+to+"/"+map[bool]string{false: "sidecar", true: "embed"}[embed], func(t *testing.T) {
					r := hops(t, fsys, prov(t, source), prov(t, to), embed, true)
					if same, diff := ir.Equal(r.src, r.back); !same {
						t.Errorf("A->B->A differs:\n%s", diff)
					}
					if childJobsOf(r.src) == 0 {
						t.Fatal("the source has no child: the test checks nothing")
					}
				})
			}
		}
	}
}

func childJobsOf(p *ir.Project) int {
	n := 0
	for _, pl := range p.Pipelines {
		for _, j := range pl.Jobs {
			if j.Child != nil {
				n++
			}
		}
	}
	return n
}

// inlined converts the GitLab source to Drone and returns the pieces of a return.
func inlined(t *testing.T) (src, back *ir.Project, m *meta.Meta) {
	t.Helper()
	a, b := prov(t, provider.GitLab), prov(t, provider.Drone)
	src, _, err := a.Parse(childSources[provider.GitLab])
	if err != nil {
		t.Fatal(err)
	}
	files, rep, err := b.Emit(src)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err = b.Parse(toFS(files))
	if err != nil {
		t.Fatal(err)
	}
	return src, back, roundtrip.NewMeta(a.ID(), src, b, rep)
}

func TestRestoreReplacesInlinedJobsWithTheCall(t *testing.T) {
	src, back, m := inlined(t)
	if len(m.Calls) != 1 {
		t.Fatalf("calls = %v, want one", m.Calls)
	}
	if orphans := roundtrip.Restore(back, m); len(orphans) != 0 {
		t.Fatalf("orphans = %v", orphans)
	}
	only := func(p *ir.Project) *ir.Project {
		for _, j := range p.Pipelines[0].Jobs {
			if j.ID == "call" {
				return &ir.Project{Pipelines: []*ir.Pipeline{{ID: "p", Jobs: []*ir.Job{j}}}}
			}
		}
		t.Fatal("no call job after the restore")
		return nil
	}
	if same, diff := ir.Equal(only(src), only(back)); !same {
		t.Errorf("the restored call differs from the source:\n%s", diff)
	}
	if got := childJobs(back); len(got) != 0 {
		t.Errorf("inlined jobs remain beside the call: %d", len(got))
	}
}

func TestRestoreReportsACallWhoseJobsAreGone(t *testing.T) {
	_, back, m := inlined(t)
	pl := back.Pipelines[0]
	pl.Jobs = slices.DeleteFunc(pl.Jobs, func(j *ir.Job) bool { return strings.HasPrefix(j.ID, "call-") })
	orphans := roundtrip.Restore(back, m)
	if !slices.Contains(orphans, "pipeline/gitlab-ci/job/call") {
		t.Fatalf("orphans = %v, want the call: an edit that removes the inlined jobs must not bring back a stale call", orphans)
	}
	if childJobsOf(back) != 0 {
		t.Error("the call must not be restored when its jobs are gone")
	}
}

func TestRestoreKeepingEditsLeavesTheInlinedJobs(t *testing.T) {
	_, back, m := inlined(t)
	_, kept := roundtrip.RestoreKeepingEdits(back, m)
	if !slices.Contains(kept, "pipeline/gitlab-ci/job/call") {
		t.Fatalf("kept = %v, want the call listed", kept)
	}
	if childJobsOf(back) != 0 || len(childJobs(back)) != 2 {
		t.Errorf("an edited file keeps its inlined jobs: %d call jobs, %d child jobs", childJobsOf(back), len(childJobs(back)))
	}
}

// TestEveryProviderRunsTheChildWork converts each source into every provider and reads the files back: the child
// work must run in each target, either as the native construct or as inline jobs.
func TestEveryProviderRunsTheChildWork(t *testing.T) {
	for source, fsys := range childSources {
		src, _, err := prov(t, source).Parse(fsys)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range provider.IDs() {
			t.Run(source+"/"+id, func(t *testing.T) {
				to := prov(t, id)
				files, _, err := to.Emit(src)
				if err != nil {
					t.Fatal(err)
				}
				back, _, err := to.Parse(toFS(files))
				if err != nil {
					t.Fatal(err)
				}
				native := id == source
				got := len(childJobs(back))
				if native {
					if childJobsOf(back) != 1 {
						t.Errorf("native target %s: %d call jobs, want 1", id, childJobsOf(back))
					}
					return
				}
				if got != 2 {
					t.Errorf("%d jobs run child work, want 2", got)
				}
			})
		}
	}
}
