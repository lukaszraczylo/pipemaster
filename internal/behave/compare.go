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

// triggerRun names the kind of run that each trigger feature starts.
var triggerRun = map[caps.Feature]string{caps.TriggerPush: kindPush, caps.TriggerPullRequest: kindPR, caps.TriggerTag: kindTag}

// inertFeatures never change what a run does. A runner label is one of them here: every engine of the harness runs a
// job on its one worker, so a label cannot change the markers of a run.
var inertFeatures = map[caps.Feature]bool{
	caps.DisplayName: true, caps.ConfigName: true, caps.PipelinePermissions: true, caps.PipelineConcurrency: true,
	caps.ProjectMultiPipeline: true, caps.JobEnvironment: true, caps.JobEnvironmentApproval: true, caps.JobRunner: true,
	caps.ExtPassthrough: true,
}

// handoffFeatures order or feed jobs.
var handoffFeatures = map[caps.Feature]bool{caps.JobNeeds: true, caps.JobNeedsArtifacts: true, caps.JobArtifacts: true}

// failureFeatures change how a failure counts.
var failureFeatures = map[caps.Feature]bool{caps.JobAllowFailure: true, caps.StepContinueOnError: true}

// disableWords mark (the wording is the emitters; no typed flag in report.Entry marks the case) a loss detail of an emitter that turns a condition it cannot state into work that never runs.
var disableWords = []string{"never runs", "disabled"}

func disablesWork(detail string) bool {
	return slices.ContainsFunc(disableWords, func(w string) bool { return strings.Contains(detail, w) })
}

// effects returns the kinds of difference a verdict can cause, with the reason for the choice.
func effects(level caps.Level, f caps.Feature) ([]string, string) {
	switch {
	case inertFeatures[f]:
		return nil, "does not change a run"
	case f == caps.StepShell && level == caps.Emulated:
		return []string{kindMissing, kindValue, kindExit}, "an image without the shell stops the step, and the work after it is missing"
	case f == caps.StepImage && level == caps.Emulated:
		return []string{kindValue}, "a default image changes the tools of the step, not whether it exits"
	case f == caps.StepAction && level == caps.External:
		return []string{kindMissing, kindValue, kindExit}, "an action that the operator wires outside the file leaves its output out"
	case conditionFeatures[f] && level != caps.External:
		return nil, "a condition verdict never explains extra work: a converted file must not run work that the source skips"
	case conditionFeatures[f]:
		return []string{kindMissing}, "a condition handled outside the file can disable work"
	case handoffFeatures[f]:
		return []string{kindMissing, kindValue, kindOrder, kindExit}, "an ordering or handoff change"
	case f == caps.JobManual && level == caps.Lost:
		return []string{kindExtra}, "a target with no manual gate starts the job by itself, so work that the source leaves to an operator runs"
	case failureFeatures[f] && level == caps.Emulated:
		return []string{kindOrder, kindExit}, "an emulated failure rule changes order and exit"
	case failureFeatures[f]:
		return []string{kindMissing, kindExit}, "a lost failure rule stops later work"
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

// printsLoud is true when the target log holds the fail-loud line of the entry, the strongest evidence of a cause.
func printsLoud(tgtLog string, e report.Entry) bool {
	return strings.Contains(tgtLog, failLoudMark+string(e.Feature)+" "+e.IRPath)
}

// cmpBool orders true before false.
func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	}
	return 1
}

// explainFact returns the rule that explains the fact, or an empty string.
func explainFact(fx Fixture, rep *report.Report, tgtLog string, f Fact, explainedOwners []string) string {
	if rep == nil {
		return ""
	}
	entries := slices.SortedStableFunc(slices.Values(rep.Entries), func(a, b report.Entry) int {
		return cmpBool(printsLoud(tgtLog, a), printsLoud(tgtLog, b))
	})
	for _, e := range entries {
		if e.Level == caps.Native {
			continue
		}
		ef, why := effects(e.Level, e.Feature)
		if conditionFeatures[e.Feature] && e.Level == caps.Lost && disablesWork(e.Detail) {
			ef, why = []string{kindMissing}, "a lost condition that the emitter fails safe removes the work it guards"
		}
		if _, isTrigger := triggerRun[e.Feature]; isTrigger && e.Level == caps.External {
			ef, why = nil, "the harness starts the run itself, so a trigger that the operator sets up changes no run"
		}
		loud := printsLoud(tgtLog, e)
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
