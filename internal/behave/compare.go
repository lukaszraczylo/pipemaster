//go:build behave

package behave

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Diff describes the first difference between two traces. Empty means the traces agree.
func Diff(src, tgt *Trace, unordered bool) string {
	a, b := src.Markers, tgt.Markers
	if unordered {
		a, b = sorted(a), sorted(b)
	}
	for i := 0; i < max(len(a), len(b)); i++ {
		switch {
		case i >= len(b):
			return fmt.Sprintf("marker %d: source has %q, target ends", i, a[i])
		case i >= len(a):
			return fmt.Sprintf("marker %d: source ends, target has %q", i, b[i])
		case a[i] != b[i]:
			return fmt.Sprintf("marker %d: source %q, target %q", i, a[i], b[i])
		}
	}
	if (src.Exit == 0) != (tgt.Exit == 0) {
		return fmt.Sprintf("exit status: source %d, target %d", src.Exit, tgt.Exit)
	}
	return ""
}

func sorted(s []string) []string {
	c := slices.Clone(s)
	sort.Strings(c)
	return c
}

// Explain returns the verdicts of the loss report that name a feature the fixture exercises. A difference that no
// entry explains is a converter bug.
func Explain(rep *report.Report, features []string) []string {
	if rep == nil {
		return nil
	}
	want := map[caps.Feature]bool{}
	for _, f := range features {
		want[caps.Feature(f)] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range rep.Entries {
		if e.Level == caps.Native || !want[e.Feature] {
			continue
		}
		k := fmt.Sprintf("%s %s", e.Level, e.Feature)
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Judge turns two traces and the loss report into one matrix cell.
func Judge(fx Fixture, src, tgt *Trace, rep *report.Report) (status, detail, explained string) {
	d := Diff(src, tgt, fx.Unordered)
	if d == "" {
		return StatusPass, "", ""
	}
	if ex := Explain(rep, fx.Features); len(ex) > 0 {
		return StatusExplained, d, strings.Join(ex, ", ")
	}
	return StatusFail, d, ""
}
