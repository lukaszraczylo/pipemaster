package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/golden"
)

func sample() *Report {
	r := &Report{Source: "gha", Target: "gitlab"}
	r.Add(Entry{IRPath: "jobs.build", Feature: caps.JobCache, Level: caps.Emulated, Strategy: "cache keyword", Detail: "key mapped", SourceLoc: "ci.yml:12"})
	r.Add(Entry{IRPath: "jobs.deploy", Feature: caps.JobEnvironmentApproval, Level: caps.Lost, Strategy: "dropped", Detail: "no approvals | pipes", SourceLoc: "ci.yml:30"})
	r.Add(Entry{IRPath: "jobs.build", Feature: caps.JobMatrix, Level: caps.Native})
	r.Add(Entry{IRPath: "jobs.build", Feature: caps.JobArtifacts, Level: caps.External, Strategy: "upload step"})
	return r
}

func render(t *testing.T, r *Report, format string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := r.Render(&b, format); err != nil {
		t.Fatalf("render %s: %v", format, err)
	}
	return b.Bytes()
}

func TestWorstEmptyIsNative(t *testing.T) {
	var nilR *Report
	if nilR.Worst() != caps.Native || (&Report{}).Worst() != caps.Native {
		t.Fatal("empty or nil report must be native")
	}
}

func TestWorstPicksLost(t *testing.T) {
	if got := sample().Worst(); got != caps.Lost {
		t.Fatalf("got %v", got)
	}
}

func TestMerge(t *testing.T) {
	r := sample()
	r.Merge(nil)
	if len(r.Entries) != 4 {
		t.Fatal("nil merge must be a no-op")
	}
	r.Merge(&Report{Entries: []Entry{{IRPath: "x", Level: caps.Lost}}})
	if len(r.Entries) != 5 {
		t.Fatalf("got %d entries", len(r.Entries))
	}
}

func TestRenderText(t *testing.T) {
	golden.Assert(t, "testdata/text.golden", render(t, sample(), "text"))
}

func TestRenderMarkdown(t *testing.T) {
	golden.Assert(t, "testdata/md.golden", render(t, sample(), "md"))
}

func TestRenderJSON(t *testing.T) {
	golden.Assert(t, "testdata/json.golden", render(t, sample(), "json"))
}

func TestRenderEmpty(t *testing.T) {
	for _, f := range []string{"text", "md"} {
		if out := render(t, &Report{Source: "a", Target: "b"}, f); !strings.Contains(string(out), "No losses") {
			t.Fatalf("%s: got %q", f, out)
		}
	}
	if out := render(t, &Report{}, "json"); !strings.Contains(string(out), `"entries": []`) {
		t.Fatalf("json: got %q", out)
	}
}

func TestRenderDoesNotMutate(t *testing.T) {
	r := sample()
	first := r.Entries[0]
	render(t, r, "text")
	if r.Entries[0] != first {
		t.Fatal("render reordered entries")
	}
}

func TestRenderUnknownFormat(t *testing.T) {
	if err := sample().Render(&bytes.Buffer{}, "xml"); err == nil {
		t.Fatal("want error")
	}
}
