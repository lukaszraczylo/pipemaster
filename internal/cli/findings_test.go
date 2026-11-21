package cli

import (
	"errors"
	"slices"
	"testing"
)

func TestSchemaFindingsIgnorePropertyOrder(t *testing.T) {
	a := schemaFindings("f.yml", errors.New("invalid\n- at '/steps/0': additional properties 'image', 'command' not allowed"))
	b := schemaFindings("f.yml", errors.New("invalid\n- at '/steps/0': additional properties 'command', 'image' not allowed"))
	if !slices.Equal(a, b) {
		t.Errorf("%q != %q", a, b)
	}
}
