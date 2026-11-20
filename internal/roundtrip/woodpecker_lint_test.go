package roundtrip_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/linttool"
)

func requireWoodpeckerCLI(t *testing.T) { linttool.RequireWoodpecker(t) }

func lintWoodpecker(t *testing.T, name string, data []byte) {
	t.Helper()
	if out, err := linttool.WoodpeckerLint(t, name, data); err != nil {
		t.Errorf("woodpecker-cli lint %s: %v\n%s", name, err, out)
	}
}

// woodpeckerFindings reports what the linter finds in one file, as lines without positions.
func woodpeckerFindings(t *testing.T, name string, data []byte) findings {
	t.Helper()
	out := findings{}
	if text, err := linttool.WoodpeckerLint(t, name, data); err != nil {
		out.add(text)
	}
	return out
}
