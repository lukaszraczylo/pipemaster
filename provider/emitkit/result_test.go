package emitkit_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

func TestNeutralizeResultWrite(t *testing.T) {
	cases := map[string]string{
		"printf yes | tee $(results.exists.path)":                  "printf yes | tee /dev/null",
		"make build && sha256sum app | tee $(results.digest.path)": "make build && sha256sum app | tee /dev/null",
		"if true; then\n  echo a > $(results.x.path)\nfi":          "if true; then\n  echo a > /dev/null\nfi",
		"echo plain": "echo plain",
	}
	for in, want := range cases {
		var lost []caps.Feature
		got := emitkit.NeutralizeResultWrite(func(_ string, f caps.Feature, _ string) { lost = append(lost, f) }, "pipeline/p/job/j/step/s", in)
		if got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
		if changed := got != in; changed != (len(lost) == 2) || (!changed && len(lost) != 0) {
			t.Errorf("%q: lost entries = %v", in, lost)
		}
		if strings.Contains(got, "results.") {
			t.Errorf("%q: result path stays", in)
		}
	}
}
