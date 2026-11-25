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

// Kinds of difference between a source trace and a target trace.
const (
	kindMissing = "missing"
	kindExtra   = "extra"
	kindValue   = "value"
	kindOrder   = "order"
	kindExit    = "exit"
)

const (
	anyOwner     = "*"
	failLoudMark = "pipemaster: lost: "
)

// Fact is one difference between two traces. Marker is the marker name, empty for an exit difference.
type Fact struct {
	Kind   string
	Marker string
	Detail string
}

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
	if src.Exit != tgt.Exit {
		return fmt.Sprintf("exit status: source %d, target %d", src.Exit, tgt.Exit)
	}
	return ""
}

func sorted(s []string) []string {
	c := slices.Clone(s)
	sort.Strings(c)
	return c
}

func markerName(m string) string {
	n, _, _ := strings.Cut(m, ":")
	return n
}

// Facts splits the difference of two traces into typed facts.
func Facts(src, tgt *Trace, unordered bool) []Fact {
	a, b := slices.Clone(src.Markers), slices.Clone(tgt.Markers)
	var missing, extra []string
	for _, m := range a {
		if i := slices.Index(b, m); i >= 0 {
			b = slices.Delete(b, i, i+1)
		} else {
			missing = append(missing, m)
		}
	}
	extra = b
	var out []Fact
	for _, m := range slices.Clone(missing) {
		j := slices.IndexFunc(extra, func(e string) bool { return markerName(e) == markerName(m) })
		if j < 0 {
			continue
		}
		out = append(out, Fact{Kind: kindValue, Marker: markerName(m), Detail: fmt.Sprintf("%q became %q", m, extra[j])})
		extra = slices.Delete(extra, j, j+1)
		missing = slices.Delete(missing, slices.Index(missing, m), slices.Index(missing, m)+1)
	}
	for _, m := range missing {
		out = append(out, Fact{Kind: kindMissing, Marker: markerName(m), Detail: fmt.Sprintf("%q is missing", m)})
	}
	for _, m := range extra {
		out = append(out, Fact{Kind: kindExtra, Marker: markerName(m), Detail: fmt.Sprintf("%q is extra", m)})
	}
	if len(out) == 0 && !unordered && !slices.Equal(src.Markers, tgt.Markers) {
		for i := range src.Markers {
			if src.Markers[i] != tgt.Markers[i] {
				out = append(out, Fact{Kind: kindOrder, Marker: markerName(src.Markers[i]), Detail: fmt.Sprintf("%q moved", src.Markers[i])})
				break
			}
		}
	}
	if src.Exit != tgt.Exit {
		out = append(out, Fact{Kind: kindExit, Detail: fmt.Sprintf("exit %d became %d", src.Exit, tgt.Exit)})
	}
	return out
}

// conditionFeatures decide whether work runs.
var conditionFeatures = map[caps.Feature]bool{
	caps.JobIf: true, caps.StepIf: true, caps.TriggerPush: true, caps.TriggerPullRequest: true, caps.TriggerTag: true,
	caps.TriggerFilterBranch: true, caps.TriggerFilterPaths: true, caps.TriggerFilterTags: true,
}

// inertFeatures never change what a run does.
var inertFeatures = map[caps.Feature]bool{
	caps.DisplayName: true, caps.ConfigName: true, caps.PipelinePermissions: true, caps.PipelineConcurrency: true,
	caps.ProjectMultiPipeline: true, caps.JobEnvironment: true, caps.JobEnvironmentApproval: true,
}

// handoffFeatures order or feed jobs.
var handoffFeatures = map[caps.Feature]bool{caps.JobNeeds: true, caps.JobNeedsArtifacts: true, caps.JobArtifacts: true}

// failureFeatures change how a failure counts.
var failureFeatures = map[caps.Feature]bool{caps.JobAllowFailure: true, caps.StepContinueOnError: true}

// effects returns the kinds of difference a verdict can cause, with the reason for the choice.
func effects(level caps.Level, f caps.Feature) ([]string, string) {
	switch {
	case inertFeatures[f]:
		return nil, "does not change a run"
	case conditionFeatures[f] && level == caps.Lost:
		return []string{kindExtra}, "a lost condition runs more work"
	case conditionFeatures[f] && level == caps.Emulated:
		return []string{kindExtra}, "an emulated condition can run more work than the source"
	case conditionFeatures[f]:
		return []string{kindExtra, kindMissing}, "a condition handled outside the file runs more or disables work"
	case handoffFeatures[f]:
		return []string{kindMissing, kindValue, kindOrder, kindExit}, "an ordering or handoff change"
	case failureFeatures[f] && level == caps.Emulated:
		return []string{kindOrder, kindExit}, "an emulated failure rule changes order and exit"
	case failureFeatures[f]:
		return []string{kindMissing, kindExit}, "a lost failure rule stops later work"
	case f == caps.JobRunner:
		return []string{kindMissing, kindExit}, "a runner label decides where a job runs and whether it starts, not what a step prints"
	}
	switch level {
	case caps.Lost:
		return []string{kindMissing, kindValue, kindExit}, "lost work is missing or changed"
	case caps.External:
		return []string{kindMissing, kindExit}, "external work is not in the file"
	case caps.Emulated:
		return []string{kindValue, kindExit}, "an emulation changes a value"
	}
	return nil, ""
}

func (f Fact) kindIn(kinds []string) bool { return slices.Contains(kinds, f.Kind) }

// owns reports whether the IR path covers the marker. A marker without a declared owner belongs to every path.
func owns(irPath string, owners map[string][]string, marker string) bool {
	tokens, ok := owners[marker]
	if !ok || marker == "" || slices.Contains(tokens, anyOwner) {
		return true
	}
	segs := strings.Split(irPath, "/")
	if !slices.Contains(segs, "job") && !slices.Contains(segs, "step") {
		return true
	}
	for _, s := range segs {
		for _, t := range tokens {
			if s == t || strings.HasPrefix(s, t+"-") {
				return true
			}
		}
	}
	return false
}

// explainFact returns the rule that explains the fact, or an empty string.
func explainFact(fx Fixture, rep *report.Report, tgtLog string, f Fact, explainedOwners []string) string {
	if rep == nil {
		return ""
	}
	for _, e := range rep.Entries {
		if e.Level == caps.Native {
			continue
		}
		ef, why := effects(e.Level, e.Feature)
		loud := strings.Contains(tgtLog, failLoudMark+string(e.Feature)+" "+e.IRPath)
		if loud {
			ef, why = []string{kindMissing, kindValue, kindExit, kindOrder}, "the target prints the fail-loud line of this entry"
		}
		if !f.kindIn(ef) {
			continue
		}
		if f.Kind == kindExit {
			if !loud && !slices.ContainsFunc(explainedOwners, func(m string) bool { return owns(e.IRPath, fx.Owners, m) }) {
				continue
			}
		} else if !owns(e.IRPath, fx.Owners, f.Marker) {
			continue
		}
		return fmt.Sprintf("%s %s at %s explains %s %s (%s)", e.Level, e.Feature, e.IRPath, f.Kind, f.Marker, why)
	}
	return ""
}

// Explain binds each fact to a verdict of the loss report. It returns the rules used and the facts left unexplained.
func Explain(fx Fixture, rep *report.Report, tgtLog string, facts []Fact) (rules []string, left []Fact) {
	var owners []string
	for _, f := range facts {
		if f.Kind == kindExit {
			continue
		}
		if r := explainFact(fx, rep, tgtLog, f, nil); r != "" {
			if !slices.Contains(rules, r) {
				rules = append(rules, r)
			}
			owners = append(owners, f.Marker)
		} else {
			left = append(left, f)
		}
	}
	for _, f := range facts {
		if f.Kind != kindExit {
			continue
		}
		if r := explainFact(fx, rep, tgtLog, f, owners); r != "" {
			rules = append(rules, r)
		} else {
			left = append(left, f)
		}
	}
	return rules, left
}

// Judge turns two traces and the loss report into one matrix cell.
func Judge(fx Fixture, src, tgt *Trace, rep *report.Report) (status, detail, explained string) {
	d := Diff(src, tgt, fx.Unordered)
	if d == "" {
		return StatusPass, "", ""
	}
	rules, left := Explain(fx, rep, tgt.Log, Facts(src, tgt, fx.Unordered))
	if len(left) == 0 && len(rules) > 0 {
		return StatusExplained, d, strings.Join(rules, "; ")
	}
	var lines []string
	for _, f := range left {
		lines = append(lines, f.Kind+": "+f.Detail)
	}
	return StatusFail, d + " | no verdict explains " + strings.Join(lines, ", "), ""
}
