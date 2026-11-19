package roundtrip_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/golden"
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

func TestIdentityDialects(t *testing.T) {
	for _, id := range []string{provider.Gitea, provider.Forgejo} {
		for _, f := range fixtures(t, id) {
			t.Run(id+"/"+f.name, func(t *testing.T) {
				p := prov(t, id)
				src, _, err := p.Parse(os.DirFS(f.dir))
				if err != nil {
					t.Fatal(err)
				}
				files, rep, err := p.Emit(src)
				if err != nil {
					t.Fatal(err)
				}
				back, _, err := p.Parse(toFS(files))
				if err != nil {
					t.Fatal(err)
				}
				// A key the dialect rejects is dropped from the file and kept in the metadata, so a lost entry means
				// the exact return needs the metadata.
				if rep.Worst() >= caps.External {
					roundtrip.Restore(back, roundtrip.NewMeta(id, src, p, rep))
				}
				if same, diff := ir.Equal(src, back); !same {
					t.Errorf("Parse->Emit->Parse differs:\n%s", diff)
				}
			})
		}
	}
}

func TestDialectAliasRoundTrip(t *testing.T) {
	dir := filepath.Join(featuresRoot, provider.Forgejo, "features", "context.alias")
	p := prov(t, provider.Forgejo)
	src, _, err := p.Parse(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ir.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, filepath.Join(dir, "ir.golden.yaml"), got)
	files, _, err := p.Emit(src)
	if err != nil {
		t.Fatal(err)
	}
	text := string(files[".forgejo/workflows/ci.yml"])
	if strings.Contains(text, "forgejo.") || strings.Contains(text, "forge.") || !strings.Contains(text, "github.ref_name") {
		t.Errorf("emitter must print github.*:\n%s", text)
	}
	back, _, err := p.Parse(toFS(files))
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(src, back); !same {
		t.Errorf("Parse->Emit->Parse differs:\n%s", diff)
	}
}

// TestDialectFixtureGoldens pins the IR of every fixture written in a dialect's own directory.
func TestDialectFixtureGoldens(t *testing.T) {
	for _, id := range []string{provider.Gitea, provider.Forgejo} {
		for _, f := range readFixtures(t, id, id) {
			t.Run(id+"/"+f.name, func(t *testing.T) {
				src, _, err := prov(t, id).Parse(os.DirFS(f.dir))
				if err != nil {
					t.Fatal(err)
				}
				got, err := ir.Marshal(src)
				if err != nil {
					t.Fatal(err)
				}
				golden.Assert(t, filepath.Join(f.dir, "ir.golden.yaml"), got)
			})
		}
	}
}

const giteaSelfWorkflow = `on: push
jobs:
  b:
    runs-on: r
    steps:
      - id: s
        if: github.ref_name == 'main'
        uses: self:o/r@v1
`

func TestGiteaSelfRoundTrip(t *testing.T) {
	fsA := fstest.MapFS{".gitea/workflows/ci.yml": &fstest.MapFile{Data: []byte(giteaSelfWorkflow)}}
	for _, embed := range []bool{false, true} {
		r := hops(t, fsA, prov(t, provider.Gitea), prov(t, provider.GHA), embed, true)
		if same, diff := ir.Equal(r.src, r.back); !same {
			t.Errorf("embed=%v: Gitea->GHA->Gitea with metadata differs:\n%s", embed, diff)
		}
		st := r.back.Pipelines[0].Jobs[0].Steps[0]
		if st.If == nil || st.Action == nil || st.Action.Host != ir.HostSelf {
			t.Errorf("embed=%v: want the original if and host back, got %+v", embed, st)
		}
	}
	r := hops(t, fsA, prov(t, provider.Gitea), prov(t, provider.GHA), false, false)
	if e := reportEntry(r.toRep, caps.StepAction); e == nil || e.Level != caps.Lost {
		t.Errorf("without metadata the lost step.action must be reported: %+v", r.toRep.Entries)
	}
}

func reportEntry(rep *report.Report, f caps.Feature) *report.Entry {
	for i := range rep.Entries {
		if rep.Entries[i].Feature == f {
			return &rep.Entries[i]
		}
	}
	return nil
}

const giteaSelfConditions = `on: push
jobs:
  authored-step:
    runs-on: r
    steps:
      - id: s
        if: false
        uses: self:o/r@v1
  plain-step:
    runs-on: r
    steps:
      - id: s
        uses: self:o/r@v1
  authored-call:
    if: false
    uses: self:o/shared/.gitea/workflows/w.yml@main
  plain-call:
    uses: self:o/shared/.gitea/workflows/w.yml@main
`

// An authored if: false and an absent condition must both survive a disabled self: step or call job.
func TestGiteaSelfKeepsAuthoredCondition(t *testing.T) {
	fsA := fstest.MapFS{".gitea/workflows/ci.yml": &fstest.MapFile{Data: []byte(giteaSelfConditions)}}
	for _, embed := range []bool{false, true} {
		r := hops(t, fsA, prov(t, provider.Gitea), prov(t, provider.GHA), embed, true)
		if same, diff := ir.Equal(r.src, r.back); !same {
			t.Errorf("embed=%v: Gitea->GHA->Gitea differs:\n%s", embed, diff)
		}
		jobs := map[string]*ir.Job{}
		for _, j := range r.back.Pipelines[0].Jobs {
			jobs[j.ID] = j
		}
		if jobs["authored-step"].Steps[0].If == nil || jobs["plain-step"].Steps[0].If != nil {
			t.Errorf("embed=%v: step conditions not restored: %+v %+v", embed, jobs["authored-step"].Steps[0].If, jobs["plain-step"].Steps[0].If)
		}
		if jobs["authored-call"].If == nil || jobs["plain-call"].If != nil {
			t.Errorf("embed=%v: job conditions not restored: %+v %+v", embed, jobs["authored-call"].If, jobs["plain-call"].If)
		}
	}
}
