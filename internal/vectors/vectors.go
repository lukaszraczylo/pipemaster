// Package vectors loads the differential test vectors that the Lean model generates.
package vectors

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Case is one generated input and the output the Lean model computes for it.
type Case struct {
	In  json.RawMessage `json:"in"`
	Out json.RawMessage `json:"out"`
}

type file struct {
	Theorem string `json:"theorem"`
	Cases   []Case `json:"cases"`
}

// Load reads testdata/lean-vectors/<name>.json and fails the test on a missing file or zero cases.
func Load(t *testing.T, name string) []Case {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate vectors package")
	}
	path := filepath.Join(filepath.Dir(here), "..", "..", "testdata", "lean-vectors", name+".json")
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed repo path plus a test-supplied name
	if err != nil {
		t.Fatalf("read vectors: %v (run `make vectors`)", err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if len(f.Cases) == 0 {
		t.Fatalf("%s holds zero cases", path)
	}
	return f.Cases
}
