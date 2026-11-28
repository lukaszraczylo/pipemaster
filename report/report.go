// Package report is the loss report printed by every conversion: one entry
// per non-native feature decision.
package report

import (
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
)

// Entry records the verdict for one feature use at one IR location.
type Entry struct {
	IRPath    string       `json:"ir_path"`
	Feature   caps.Feature `json:"feature"`
	Strategy  string       `json:"strategy"`
	Detail    string       `json:"detail"`
	SourceLoc string       `json:"source_loc"`
	Level     caps.Level   `json:"level"`
}

// Report collects the entries of one conversion.
type Report struct {
	Source  string  `json:"source"`
	Target  string  `json:"target"`
	Entries []Entry `json:"entries"`
	// Retained holds source fields the round-trip metadata keeps although the target file holds an equivalent,
	// so they are no loss to report. They stay out of the printed and JSON report.
	Retained []Entry `json:"-"`
	// Withdrawn names entries of an earlier stage that this stage makes untrue. Merge drops each entry of the
	// receiver with the same IR path, feature and strategy.
	Withdrawn []Entry `json:"-"`
}

// Add appends e.
func (r *Report) Add(e Entry) { r.Entries = append(r.Entries, e) }

// Merge appends the entries of o; a nil o is a no-op.
func (r *Report) Merge(o *Report) {
	if o == nil {
		return
	}
	if len(o.Withdrawn) > 0 {
		r.Entries = slices.DeleteFunc(r.Entries, func(e Entry) bool {
			return slices.ContainsFunc(o.Withdrawn, func(w Entry) bool {
				return w.IRPath == e.IRPath && w.Feature == e.Feature && w.Strategy == e.Strategy
			})
		})
	}
	r.Entries = append(r.Entries, o.Entries...)
	r.Retained = append(r.Retained, o.Retained...)
}

// Worst returns the highest level present, or Native when there are none.
func (r *Report) Worst() caps.Level {
	worst := caps.Native
	if r == nil {
		return worst
	}
	for _, e := range r.Entries {
		if e.Level > worst {
			worst = e.Level
		}
	}
	return worst
}
