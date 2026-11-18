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

func TestLedgerNoteThenDecideKeepsDowngrade(t *testing.T) {
	l := NewLedger("a", "b", caps.Matrix{caps.TriggerPush: {Level: caps.Native}})
	u := use("p", caps.TriggerPush)
	l.Note(u, caps.Support{Level: caps.Lost, Strategy: "s"}, "dropped")
	if s := l.Decide(u); s.Level != caps.Lost {
		t.Fatalf("Decide returned %v, want lost", s.Level)
	}
	r, err := l.Finish([]ir.FeatureUse{u})
	if err != nil || len(r.Entries) != 1 || r.Entries[0].Level != caps.Lost || r.Entries[0].Detail != "dropped" {
		t.Fatalf("err=%v entries=%+v", err, r.Entries)
	}
	if len(l.Emitted()) != 0 {
		t.Fatal("downgraded use must not be emitted")
	}
}

func TestLedgerCompleteness(t *testing.T) {
	levels := []caps.Level{caps.Native, caps.Emulated, caps.External, caps.Lost}
	feats := []caps.Feature{caps.TriggerPush, caps.TriggerTag, caps.TriggerAPI, caps.PipelineEnv}
	type op struct {
		u      ir.FeatureUse
		lvl    caps.Level
		isNote bool
	}
	rapid.Check(t, func(t *rapid.T) {
		m := caps.Matrix{}
		for _, f := range feats {
			if rapid.Bool().Draw(t, "declared") {
				m[f] = caps.Support{Level: rapid.SampledFrom(levels).Draw(t, "lvl")}
			}
		}
		pool := rapid.SliceOfNDistinct(
			rapid.Custom(func(t *rapid.T) ir.FeatureUse {
				return use(rapid.StringMatching(`[a-c]{1,2}`).Draw(t, "path"), rapid.SampledFrom(feats).Draw(t, "f"))
			}), 1, 6, func(u ir.FeatureUse) ir.FeatureUse { return u }).Draw(t, "pool")
		n := rapid.IntRange(len(pool), 20).Draw(t, "n")
		ops := make([]op, 0, n)
		for _, u := range pool {
			ops = append(ops, op{u: u})
		}
		for len(ops) < n {
			ops = append(ops, op{u: rapid.SampledFrom(pool).Draw(t, "u"), isNote: rapid.Bool().Draw(t, "note"), lvl: rapid.SampledFrom(levels).Draw(t, "nl")})
		}
		ops = rapid.Permutation(ops).Draw(t, "order")

		want := map[ir.FeatureUse]caps.Level{}
		l := NewLedger("s", "t", m)
		for _, o := range ops {
			if _, ok := want[o.u]; !ok {
				want[o.u] = m.Lookup(o.u.Feature).Level
			}
			if o.isNote {
				l.Note(o.u, caps.Support{Level: o.lvl}, "x")
				want[o.u] = max(want[o.u], o.lvl)
			} else {
				l.Decide(o.u)
			}
		}
		r, err := l.Finish(pool)
		if err != nil {
			t.Fatal(err)
		}
		emitted := map[ir.FeatureUse]bool{}
		for _, u := range l.Emitted() {
			emitted[u] = true
		}
		reported := map[ir.FeatureUse]caps.Level{}
		count := map[ir.FeatureUse]int{}
		for _, e := range r.Entries {
			k := use(e.IRPath, e.Feature)
			reported[k] = e.Level
			count[k]++
		}
		for u, lvl := range want {
			if lvl == caps.Native {
				if !emitted[u] || count[u] != 0 {
					t.Fatalf("%v: native, emitted=%v reports=%d", u, emitted[u], count[u])
				}
				continue
			}
			if emitted[u] || count[u] != 1 || reported[u] != lvl {
				t.Fatalf("%v: want level %v once, emitted=%v reports=%d level=%v", u, lvl, emitted[u], count[u], reported[u])
			}
		}
	})
}
