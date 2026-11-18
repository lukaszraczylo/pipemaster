// Package emitkit holds helpers every emitter uses to keep the no-silent-loss rule.
package emitkit

import (
	"errors"
	"fmt"
	"sort"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Ledger accounts for every feature use of one emission.
//
// Model (mirrored by Pipemaster.Ledger.complete):
//   - Decide(u) accounts u at the matrix level; Native means emitted, anything else adds a report entry.
//   - Note(u, s, d) raises u's effective level to max(current, s.Level); it never lowers it.
//   - A use has at most one entry; a higher level replaces the earlier entry.
//   - Finish(uses) errors for every use never passed to Decide or Note.
type Ledger struct {
	m      caps.Matrix
	state  map[ir.FeatureUse]report.Entry
	source string
	target string
}

// NewLedger starts a ledger for a source-to-target conversion against target matrix m.
func NewLedger(source, target string, m caps.Matrix) *Ledger {
	return &Ledger{source: source, target: target, m: m, state: map[ir.FeatureUse]report.Entry{}}
}

func entryFor(u ir.FeatureUse, s caps.Support, detail string) report.Entry {
	return report.Entry{IRPath: u.IRPath, Feature: u.Feature, Level: s.Level, Strategy: s.Strategy, Detail: detail}
}

// Decide accounts u at the target matrix level and returns the support so the emitter knows what to do.
func (l *Ledger) Decide(u ir.FeatureUse) caps.Support {
	s := l.m.Lookup(u.Feature)
	l.state[u] = entryFor(u, s, s.Caveat)
	return s
}

// Note records an emitter-chosen downgrade; the effective level is never lower than the matrix level.
func (l *Ledger) Note(u ir.FeatureUse, s caps.Support, detail string) {
	cur, ok := l.state[u]
	if !ok {
		base := l.m.Lookup(u.Feature)
		cur = entryFor(u, base, base.Caveat)
	}
	if s.Level >= cur.Level {
		cur = entryFor(u, s, detail)
	}
	l.state[u] = cur
}

// Emitted returns the uses accounted at Native level, sorted.
func (l *Ledger) Emitted() []ir.FeatureUse {
	var out []ir.FeatureUse
	for u, e := range l.state {
		if e.Level == caps.Native {
			out = append(out, u)
		}
	}
	sortUses(out)
	return out
}

// Finish returns the report, and an error naming every use in uses that was never accounted.
func (l *Ledger) Finish(uses []ir.FeatureUse) (*report.Report, error) {
	r := &report.Report{Source: l.source, Target: l.target}
	for _, e := range l.state {
		if e.Level != caps.Native {
			r.Add(e)
		}
	}
	sort.Slice(r.Entries, func(i, j int) bool {
		a, b := r.Entries[i], r.Entries[j]
		if a.IRPath != b.IRPath {
			return a.IRPath < b.IRPath
		}
		return a.Feature < b.Feature
	})
	missing := append([]ir.FeatureUse(nil), uses...)
	sortUses(missing)
	var errs []error
	for _, u := range missing {
		if _, ok := l.state[u]; !ok {
			errs = append(errs, fmt.Errorf("unaccounted feature use: %s %s", u.IRPath, u.Feature))
		}
	}
	return r, errors.Join(errs...)
}

func sortUses(us []ir.FeatureUse) {
	sort.Slice(us, func(i, j int) bool {
		if us[i].IRPath != us[j].IRPath {
			return us[i].IRPath < us[j].IRPath
		}
		return us[i].Feature < us[j].Feature
	})
}
