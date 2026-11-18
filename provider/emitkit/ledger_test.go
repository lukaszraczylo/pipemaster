package emitkit

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"pgregory.net/rapid"
)

func use(p string, f caps.Feature) ir.FeatureUse { return ir.FeatureUse{IRPath: p, Feature: f} }

func TestLedgerNativeNoEntry(t *testing.T) {
	m := caps.Matrix{caps.TriggerPush: {Level: caps.Native}}
	l := NewLedger("gha", "gitlab", m)
	u := use("on.push", caps.TriggerPush)
	if s := l.Decide(u); s.Level != caps.Native {
		t.Fatalf("level %v", s.Level)
	}
	r, err := l.Finish([]ir.FeatureUse{u})
	if err != nil || len(r.Entries) != 0 {
		t.Fatalf("err=%v entries=%v", err, r.Entries)
	}
	if r.Source != "gha" || r.Target != "gitlab" {
		t.Fatalf("source/target %q %q", r.Source, r.Target)
	}
	if e := l.Emitted(); len(e) != 1 || e[0] != u {
		t.Fatalf("emitted %v", e)
	}
}

func TestLedgerLostAddsEntry(t *testing.T) {
	l := NewLedger("a", "b", caps.Matrix{})
	u := use("p", caps.TriggerAPI)
	l.Decide(u)
	r, err := l.Finish([]ir.FeatureUse{u})
	if err != nil || len(r.Entries) != 1 {
		t.Fatalf("err=%v entries=%v", err, r.Entries)
	}
	e := r.Entries[0]
	if e.Level != caps.Lost || e.IRPath != "p" || e.Feature != caps.TriggerAPI || e.Detail != "not declared by target" {
		t.Fatalf("entry %+v", e)
	}
	if len(l.Emitted()) != 0 {
		t.Fatal("lost use must not be emitted")
	}
}

func TestLedgerFinishUnaccounted(t *testing.T) {
	l := NewLedger("a", "b", caps.Matrix{})
	l.Decide(use("p1", caps.TriggerAPI))
	_, err := l.Finish([]ir.FeatureUse{use("p1", caps.TriggerAPI), use("p2", caps.TriggerPush), use("p3", caps.TriggerTag)})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"unaccounted feature use: p2 trigger.push", "unaccounted feature use: p3 trigger.tag"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "p1") {
		t.Error("p1 was accounted")
	}
}

func TestLedgerNoteCannotLower(t *testing.T) {
	m := caps.Matrix{caps.TriggerPush: {Level: caps.External, Strategy: "ext"}}
	l := NewLedger("a", "b", m)
	u := use("p", caps.TriggerPush)
	l.Decide(u)
	l.Note(u, caps.Support{Level: caps.Native, Strategy: "claimed native"}, "d")
	r, err := l.Finish([]ir.FeatureUse{u})
	if err != nil || len(r.Entries) != 1 || r.Entries[0].Level != caps.External {
		t.Fatalf("err=%v entries=%+v", err, r.Entries)
	}
	l.Note(u, caps.Support{Level: caps.Lost, Strategy: "worse"}, "dropped")
	r, _ = l.Finish([]ir.FeatureUse{u})
	if len(r.Entries) != 1 || r.Entries[0].Level != caps.Lost || r.Entries[0].Detail != "dropped" || r.Entries[0].Strategy != "worse" {
		t.Fatalf("entries=%+v", r.Entries)
	}
}

func TestLedgerNoteWithoutDecideAccounts(t *testing.T) {
	l := NewLedger("a", "b", caps.Matrix{caps.TriggerPush: {Level: caps.Native}})
	u := use("p", caps.TriggerPush)
	l.Note(u, caps.Support{Level: caps.Emulated, Strategy: "s"}, "d")
	r, err := l.Finish([]ir.FeatureUse{u})
	if err != nil || len(r.Entries) != 1 || r.Entries[0].Level != caps.Emulated {
		t.Fatalf("err=%v entries=%+v", err, r.Entries)
	}
	if len(l.Emitted()) != 0 {
		t.Fatal("downgraded use must not be emitted")
	}
}

func TestLedgerCompleteness(t *testing.T) {
	levels := []caps.Level{caps.Native, caps.Emulated, caps.External, caps.Lost}
	feats := []caps.Feature{caps.TriggerPush, caps.TriggerTag, caps.TriggerAPI, caps.PipelineEnv}
	rapid.Check(t, func(t *rapid.T) {
		m := caps.Matrix{}
		for _, f := range feats {
			if rapid.Bool().Draw(t, "declared") {
				m[f] = caps.Support{Level: rapid.SampledFrom(levels).Draw(t, "lvl")}
			}
		}
		n := rapid.IntRange(0, 12).Draw(t, "n")
		var uses []ir.FeatureUse
		seen := map[ir.FeatureUse]bool{}
		for i := 0; i < n; i++ {
			u := use(rapid.StringMatching(`[a-c]{1,2}`).Draw(t, "path"), rapid.SampledFrom(feats).Draw(t, "f"))
			if !seen[u] {
				seen[u] = true
				uses = append(uses, u)
			}
		}
		l := NewLedger("s", "t", m)
		for _, u := range uses {
			l.Decide(u)
			if rapid.Bool().Draw(t, "note") {
				l.Note(u, caps.Support{Level: rapid.SampledFrom(levels).Draw(t, "nl")}, "x")
			}
		}
		r, err := l.Finish(uses)
		if err != nil {
			t.Fatal(err)
		}
		emitted := map[ir.FeatureUse]bool{}
		for _, u := range l.Emitted() {
			emitted[u] = true
		}
		reported := map[ir.FeatureUse]int{}
		for _, e := range r.Entries {
			reported[use(e.IRPath, e.Feature)]++
		}
		for _, u := range uses {
			if emitted[u] == (reported[u] > 0) || reported[u] > 1 {
				t.Fatalf("use %v emitted=%v reported=%d", u, emitted[u], reported[u])
			}
		}
	})
}
