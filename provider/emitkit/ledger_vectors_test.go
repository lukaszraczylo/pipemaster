package emitkit_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

type ledgerUse struct {
	IRPath  string `json:"ir_path"`
	Feature string `json:"feature"`
}

type ledgerSupport struct {
	Level    string `json:"level"`
	Strategy string `json:"strategy"`
	Caveat   string `json:"caveat"`
}

type ledgerMatrixEntry struct {
	Feature string        `json:"feature"`
	Support ledgerSupport `json:"support"`
}

type ledgerOp struct {
	Op      string        `json:"op"`
	Use     ledgerUse     `json:"use"`
	Support ledgerSupport `json:"support"`
	Detail  string        `json:"detail"`
}

type ledgerIn struct {
	Matrix []ledgerMatrixEntry `json:"matrix"`
	Ops    []ledgerOp          `json:"ops"`
	Uses   []ledgerUse         `json:"uses"`
}

type ledgerEntry struct {
	IRPath   string `json:"ir_path"`
	Feature  string `json:"feature"`
	Level    string `json:"level"`
	Strategy string `json:"strategy"`
	Detail   string `json:"detail"`
}

type ledgerOut struct {
	Emitted     []ledgerUse   `json:"emitted"`
	Report      []ledgerEntry `json:"report"`
	Unaccounted []ledgerUse   `json:"unaccounted"`
}

func (s ledgerSupport) toCaps(t *testing.T, idx int) caps.Support {
	t.Helper()
	lv, err := caps.ParseLevel(s.Level)
	if err != nil {
		t.Fatalf("case %d: %v", idx, err)
	}
	return caps.Support{Level: lv, Strategy: s.Strategy, Caveat: s.Caveat}
}

func (u ledgerUse) toIR() ir.FeatureUse {
	return ir.FeatureUse{IRPath: u.IRPath, Feature: caps.Feature(u.Feature)}
}

func usesOf(us []ir.FeatureUse) []ledgerUse {
	out := make([]ledgerUse, len(us))
	for i, u := range us {
		out[i] = ledgerUse{IRPath: u.IRPath, Feature: string(u.Feature)}
	}
	return out
}

const unaccountedPrefix = "unaccounted feature use: "

func unaccountedOf(err error) []ledgerUse {
	if err == nil {
		return []ledgerUse{}
	}
	var joined interface{ Unwrap() []error }
	errs := []error{err}
	if errors.As(err, &joined) {
		errs = joined.Unwrap()
	}
	out := make([]ledgerUse, 0, len(errs))
	for _, e := range errs {
		rest := strings.TrimPrefix(e.Error(), unaccountedPrefix)
		path, feat, _ := strings.Cut(rest, " ")
		out = append(out, ledgerUse{IRPath: path, Feature: feat})
	}
	return out
}

func TestLedgerVectors(t *testing.T) {
	cases := vectors.Load(t, "ledger")
	for i, c := range cases {
		var in ledgerIn
		if err := json.Unmarshal(c.In, &in); err != nil {
			t.Fatalf("case %d: decode in: %v", i, err)
		}
		var want ledgerOut
		if err := json.Unmarshal(c.Out, &want); err != nil {
			t.Fatalf("case %d: decode out: %v", i, err)
		}
		m := caps.Matrix{}
		for _, e := range in.Matrix {
			m[caps.Feature(e.Feature)] = e.Support.toCaps(t, i)
		}
		l := emitkit.NewLedger("src", "dst", m)
		for _, op := range in.Ops {
			switch op.Op {
			case "decide":
				l.Decide(op.Use.toIR())
			case "note":
				l.Note(op.Use.toIR(), op.Support.toCaps(t, i), op.Detail)
			default:
				t.Fatalf("case %d: unknown op %q", i, op.Op)
			}
		}
		uses := make([]ir.FeatureUse, len(in.Uses))
		for k, u := range in.Uses {
			uses[k] = u.toIR()
		}
		emitted := usesOf(l.Emitted())
		rep, err := l.Finish(uses)

		gotReport := make([]ledgerEntry, len(rep.Entries))
		for k, e := range rep.Entries {
			gotReport[k] = ledgerEntry{IRPath: e.IRPath, Feature: string(e.Feature), Level: e.Level.String(), Strategy: e.Strategy, Detail: e.Detail}
		}
		got := ledgerOut{Emitted: emitted, Report: gotReport, Unaccounted: unaccountedOf(err)}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("case %d: in=%s: ledger differs (-lean +go):\n%s", i, c.In, diff)
		}
	}
	t.Logf("%d cases", len(cases))
}
