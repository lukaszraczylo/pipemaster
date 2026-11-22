package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider/concourse"
)

func TestDecodeRejectsDuplicateKeys(t *testing.T) {
	cases := map[string]string{
		"top level":         "jobs: []\njobs: []\n",
		"nested in a step":  "jobs:\n- name: j\n  plan:\n  - get: r\n    passed: [a]\n    passed: [b]\n",
		"in an opaque map":  "resources:\n- name: r\n  source: {uri: a, uri: b}\n",
		"inside a sequence": "x:\n- a: 1\n  b: 2\n  a: 3\n",
	}
	for name, y := range cases {
		t.Run(name, func(t *testing.T) {
			// A struct without the repeated field and an untyped target both hide it from yaml.v3, so the helper
			// must find it on the node tree whatever the target.
			var typed struct {
				Jobs []any `yaml:"jobs"`
			}
			err := concourse.Decode([]byte(y), &typed)
			if err == nil || !strings.Contains(err.Error(), "already defined") {
				t.Fatalf("got %v, want an already defined error", err)
			}
			if _, err := concourse.DecodeNode([]byte(y)); err == nil || !strings.Contains(err.Error(), "already defined") {
				t.Fatalf("DecodeNode: got %v, want an already defined error", err)
			}
		})
	}
}

func TestDecodeDuplicateKeyReportsLines(t *testing.T) {
	_, err := concourse.DecodeNode([]byte("a: 1\nb: 2\na: 3\n"))
	if err == nil || !strings.Contains(err.Error(), `mapping key "a" already defined at line 1`) || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("got %v", err)
	}
}

func TestDecodeAcceptsDuplicateAnchors(t *testing.T) {
	y := "a: &x 1\nb: &x 2\nc: *x\n"
	var got map[string]int
	if err := concourse.Decode([]byte(y), &got); err != nil {
		t.Fatal(err)
	}
	if got["c"] != 2 {
		t.Fatalf("an alias must resolve to the latest anchor, got %d", got["c"])
	}
}

func TestDecodeAcceptsMergeKeys(t *testing.T) {
	y := "base: &b {k: 1, m: 2}\nd:\n  <<: *b\n  k: 3\n"
	var got map[string]map[string]int
	if err := concourse.Decode([]byte(y), &got); err != nil {
		t.Fatal(err)
	}
	if got["d"]["k"] != 3 || got["d"]["m"] != 2 {
		t.Fatalf("a merge key supplies the base values and an explicit key wins, got %v", got["d"])
	}
}

func TestDecodeAcceptsRepeatedMergeKeyInOneMap(t *testing.T) {
	y := "a: &a {x: 1}\nb: &b {y: 2}\nd:\n  <<: *a\n  <<: *b\n"
	if _, err := concourse.DecodeNode([]byte(y)); err != nil {
		t.Fatalf("a merge key is not a duplicate: %v", err)
	}
}

func TestDecodeSameKeyInDifferentMapsPasses(t *testing.T) {
	var got any
	if err := concourse.Decode([]byte("a: {k: 1}\nb: {k: 2}\n"), &got); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRejectsMultipleDocuments(t *testing.T) {
	if _, err := concourse.DecodeNode([]byte("a: 1\n---\nb: 2\n")); err == nil {
		t.Fatal("a Concourse file holds one document")
	}
}

func TestDecodeEmptyInput(t *testing.T) {
	if _, err := concourse.DecodeNode(nil); err == nil {
		t.Fatal("empty input must be an error")
	}
}
