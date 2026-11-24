package roundtrip_test

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	budgetFile  = "../../testdata/site/budgets.yaml"
	budgetWrite = "PIPEMASTER_WRITE_BUDGETS"
	// failLoudMark starts the message of every failing script; a failing script for a lost script line names script.interpolation.
	failLoudMark = "pipemaster: lost: "
	lostLineMark = failLoudMark + "script.interpolation "
)

// budget counts, per site sample and target, the places where a converted pipeline does less than the source.
type budget struct {
	// Placeholder counts failing steps that stand in for a step or job with no target form.
	Placeholder int `yaml:"placeholder"`
	// LostLine counts failing lines that stand in for a script statement with no target spelling.
	LostLine int `yaml:"lost_line"`
	// Disabled counts jobs and steps that stay disabled because their condition has no target form.
	Disabled int `yaml:"disabled"`
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
		b.LostLine += strings.Count(f.Text, lostLineMark)
		b.Placeholder += strings.Count(f.Text, failLoudMark) - strings.Count(f.Text, lostLineMark)
		b.Disabled += len(disabledMarks[target].FindAllStringIndex(f.Text, -1))
	}
	return b
}

// measureBudgets converts every site sample to every target and counts what each file holds.
func measureBudgets(t *testing.T) map[string]map[string]budget {
	t.Helper()
	out := map[string]map[string]budget{}
	for _, s := range siteSamples {
		out[s.Key] = map[string]budget{}
		for _, c := range siteConvert(t, s).Conversions {
			out[s.Key][c.ID] = countBudget(c.ID, c.Files)
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
// testdata/site/budgets.yaml allows. A lower count only prints a hint.
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
			for _, c := range []struct {
				name      string
				got, want int
			}{{"placeholder", g.Placeholder, w.Placeholder}, {"lost_line", g.LostLine, w.LostLine}, {"disabled", g.Disabled, w.Disabled}} {
				switch {
				case c.got > c.want:
					t.Errorf("%s>%s %s = %d, budget %d: a conversion lost more than it may", src, to, c.name, c.got, c.want)
				case c.got < c.want:
					t.Logf("hint: %s>%s %s dropped to %d; lower its budget from %d in %s", src, to, c.name, c.got, c.want, budgetFile)
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
