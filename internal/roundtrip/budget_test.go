package roundtrip_test

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	budgetFile  = "../../testdata/site/budgets.yaml"
	budgetWrite = "PIPEMASTER_WRITE_BUDGETS"
	// failLoudMark starts the message of every failing script; a failing script for a lost script line names script.interpolation.
	failLoudMark = "pipemaster: lost: "
	lostLineMark = failLoudMark + "script.interpolation "
)

// lostStepComment matches the comment that keeps the source of a step with no target form, in the shape of every
// target: "step <path or id> uses|kind|has kind|downloads ..." (GitLab, Drone, Woodpecker, Tekton) and "uses: <ref>"
// (Argo, Concourse). A disabled step has a comment of its own. Group 1 is the step, group 2 the reference.
var lostStepComment = regexp.MustCompile(`^# pipemaster:lost (?:step (\S+) (?:uses|kind|has kind|downloads)\b|uses: (\S+))`)

// failingLineFor names what a failing line must hold to answer a lost-step comment: the step path (or its id),
// or the action reference when the comment carries only that.
func failingLineFor(m []string) string {
	switch {
	case strings.Contains(m[1], "/step/"):
		return m[1] + ":"
	case m[1] != "":
		return "/step/" + m[1] + ":"
	}
	return m[2]
}

// budgetSamples are fixtures that only the budget test converts, beside the site samples.
var budgetSamples = []siteSample{
	{Key: "mixed-lost-steps", From: provider.GHA, Title: "lost steps between running steps", dir: "../../testdata/budget/mixed-lost-steps"},
}

// budget counts, per site sample and target, the places where a converted pipeline does less than the source.
type budget struct {
	// Placeholder counts failing steps that stand in for a step or job with no target form.
	Placeholder int `yaml:"placeholder"`
	// LostLine counts failing lines that stand in for a script statement with no target spelling.
	LostLine int `yaml:"lost_line"`
	// Disabled counts jobs and steps that stay disabled because their condition has no target form.
	Disabled int `yaml:"disabled"`
	// Silent counts lost-step comments that no failing step answers. It has no budget: it must be 0.
	Silent int `yaml:"-"`
}

// disabledMarks holds, per target, the text that a disabled job or step leaves in the file.
var disabledMarks = map[string]*regexp.Regexp{
	"gha":        regexp.MustCompile(`(?m)^\s*(- )?if: false\s*$`),
	"gitlab":     regexp.MustCompile(`when: never`),
	"woodpecker": regexp.MustCompile(`(?m)evaluate: ['"]?false['"]?\s*$`),
	"drone":      regexp.MustCompile(`exclude:\s*\n\s*-\s+['"]?\*\*`),
	"tekton":     regexp.MustCompile(`pipemaster-disabled`),
	"argo":       regexp.MustCompile(`\{\{= false \}\}|exit 0 # pipemaster:lost`),
	"concourse":  regexp.MustCompile(`exit 0 # pipemaster:guard`),
}

// countBudget measures the files of one conversion.
func countBudget(target string, files []siteFile) budget {
	var b budget
	for _, f := range files {
		lostLines, steps := 0, 0
		var needles, failing []string
		for _, line := range strings.Split(f.Text, "\n") {
			// A marker copy in a comment holds the same text as the line it describes: count what runs.
			if text := strings.TrimSpace(line); strings.HasPrefix(text, "#") {
				if m := lostStepComment.FindStringSubmatch(text); m != nil {
					needles = append(needles, failingLineFor(m))
				}
				continue
			}
			lostLines += strings.Count(line, lostLineMark)
			steps += strings.Count(line, failLoudMark) - strings.Count(line, lostLineMark)
			if strings.Contains(line, failLoudMark) {
				failing = append(failing, line)
			}
		}
		// Each comment needs its own failing line for the same step, so an unrelated failing step cannot answer it.
		for _, n := range needles {
			i := slices.IndexFunc(failing, func(l string) bool { return strings.Contains(l, n) })
			if i < 0 {
				b.Silent++
				continue
			}
			failing = slices.Delete(failing, i, i+1)
		}
		b.LostLine += lostLines
		b.Placeholder += steps
		b.Disabled += len(disabledMarks[target].FindAllStringIndex(f.Text, -1))
	}
	return b
}

// measureBudgets converts every site sample to every target and counts what each file holds.
func measureBudgets(t *testing.T) map[string]map[string]budget {
	t.Helper()
	out := map[string]map[string]budget{}
	for _, s := range append(slices.Clone(siteSamples), budgetSamples...) {
		out[s.Key] = map[string]budget{}
		for _, id := range siteTargets {
			if id == s.From {
				continue
			}
			dir := t.TempDir()
			if code, _, stderr := runOut("convert", s.dir, "--from", s.From, "--to", id, "-o", dir); code != 0 {
				t.Fatalf("%s: convert --to %s = %d\n%s", s.Key, id, code, stderr)
			}
			out[s.Key][id] = countBudget(id, siteTree(t, dir))
		}
	}
	return out
}

func writeBudgets(t *testing.T, got map[string]map[string]budget) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("# Per site sample and target: the most failing steps (placeholder), failing script lines (lost_line)\n")
	sb.WriteString("# and disabled jobs or steps (disabled) a conversion may hold. Lower a number when TestPlaceholderBudgets\n")
	sb.WriteString("# says a count dropped. Raising a number needs an explicit edit and a reason in the commit.\n")
	for _, src := range sortedKeys(got) {
		fmt.Fprintf(&sb, "%s:\n", src)
		for _, to := range sortedKeys(got[src]) {
			b := got[src][to]
			fmt.Fprintf(&sb, "  %s: {placeholder: %d, lost_line: %d, disabled: %d}\n", to, b.Placeholder, b.LostLine, b.Disabled)
		}
	}
	if err := os.WriteFile(budgetFile, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestPlaceholderBudgets fails when a conversion holds more failing steps, failing lines or disabled jobs than
// testdata/site/budgets.yaml allows. A lower count fails too, so a failing line that disappears cannot pass as an improvement.
func TestPlaceholderBudgets(t *testing.T) {
	got := measureBudgets(t)
	if os.Getenv(budgetWrite) != "" {
		writeBudgets(t, got)
		return
	}
	raw, err := os.ReadFile(budgetFile) // #nosec G304 -- fixed fixture path
	if err != nil {
		t.Fatalf("read budgets: %v; set %s=1 to write them", err, budgetWrite)
	}
	var want map[string]map[string]budget
	if err := yaml.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode %s: %v", budgetFile, err)
	}
	for _, src := range sortedKeys(got) {
		for _, to := range sortedKeys(got[src]) {
			g, w := got[src][to], want[src][to]
			if _, ok := want[src][to]; !ok {
				t.Errorf("%s>%s has no budget in %s", src, to, budgetFile)
				continue
			}
			if g.Silent > 0 {
				t.Errorf("%s>%s keeps %d lost step comments with no failing step: a converted pipeline skips them without a sign", src, to, g.Silent)
			}
			for _, c := range []struct {
				name      string
				got, want int
			}{{"placeholder", g.Placeholder, w.Placeholder}, {"lost_line", g.LostLine, w.LostLine}, {"disabled", g.Disabled, w.Disabled}} {
				switch {
				case c.got > c.want:
					t.Errorf("%s>%s %s = %d, budget %d: a conversion lost more than it may", src, to, c.name, c.got, c.want)
				case c.got < c.want:
					t.Errorf("%s>%s %s dropped to %d from %d: lower the budget to %d in %s (a silent drop must not pass)", src, to, c.name, c.got, c.want, c.got, budgetFile)
				}
			}
		}
	}
	for src, row := range want {
		for to := range row {
			if _, ok := got[src][to]; !ok {
				t.Errorf("budget %s>%s names a pair the site no longer converts", src, to)
			}
		}
	}
}

// The budget counts the same text that the emitter writes for a failing script.
func TestBudgetMarksMatchTheEmitter(t *testing.T) {
	line := emitkit.FailLoud("script.interpolation", "pipeline/p", "x")
	if !strings.Contains(line, lostLineMark) || !strings.Contains(line, failLoudMark) {
		t.Fatalf("emitter text %q does not carry the budget marks", line)
	}
}
