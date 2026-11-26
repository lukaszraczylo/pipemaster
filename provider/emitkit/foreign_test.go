package emitkit_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

func TestForeignExtRetainsCarriedKeysWithoutAnEntry(t *testing.T) {
	l := emitkit.NewLedger("a", "b", caps.Matrix{})
	ext := ir.Ext{"other": map[string]any{"gets": 1, "public": true, emitkit.CarriedKey: []any{"gets"}}}
	emitkit.ForeignExt(l, "own", "B", "pipeline/p/job/j", ext)
	rep, err := l.Finish(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Entries) != 1 || !strings.Contains(rep.Entries[0].Detail, "(public)") {
		t.Fatalf("only the uncarried key is reported: %+v", rep.Entries)
	}
	if len(rep.Retained) != 1 || !strings.Contains(rep.Retained[0].Detail, "(gets)") {
		t.Fatalf("the carried key is retained: %+v", rep.Retained)
	}
}

func TestMeaningfulSkipsCarriedKeys(t *testing.T) {
	tests := []struct {
		name string
		ext  ir.Ext
		want []string
	}{
		{"behaviour key reports", ir.Ext{"other": map[string]any{"gets": 1}}, []string{"other"}},
		{"carried key is silent", ir.Ext{"other": map[string]any{"gets": 1, emitkit.CarriedKey: []any{"gets"}}}, nil},
		{"carried list as strings", ir.Ext{"other": map[string]any{"gets": 1, emitkit.CarriedKey: []string{"gets"}}}, nil},
		{"one carried, one not", ir.Ext{"other": map[string]any{"gets": 1, "public": true, emitkit.CarriedKey: []any{"gets"}}}, []string{"other"}},
		{"own provider is skipped", ir.Ext{"own": map[string]any{"gets": 1}}, nil},
		{"non-map value reports", ir.Ext{"other": "x"}, []string{"other"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := emitkit.Meaningful(tc.ext, "own")
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
