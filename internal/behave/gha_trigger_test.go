//go:build behave

package behave

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGhaAdmits(t *testing.T) {
	push := Event{Kind: kindPush, Branch: "main"}
	tag := Event{Kind: kindTag, Tag: "v1.0.0"}
	pr := Event{Kind: kindPR, Branch: "feature/x", Target: "main"}
	for name, tc := range map[string]struct {
		file string
		ev   Event
		want bool
	}{
		"list form admits a tag":                  {"on: [push, pull_request]\n", tag, true},
		"branches only skips a tag":               {"on:\n  push:\n    branches:\n      - '**'\n", tag, false},
		"branches only admits a branch":           {"on:\n  push:\n    branches:\n      - '**'\n", push, true},
		"tags only skips a branch":                {"on:\n  push:\n    tags: ['v*']\n", push, false},
		"tags only admits a tag":                  {"on:\n  push:\n    tags: ['v*']\n", tag, true},
		"both kinds admit either":                 {"on:\n  push:\n    branches: [main]\n    tags: ['v*']\n", tag, true},
		"branches-ignore skips the ignored one":   {"on:\n  push:\n    branches-ignore: [main]\n", push, false},
		"no push key skips a push":                {"on:\n  pull_request:\n", push, false},
		"pull request matches the base branch":    {"on:\n  pull_request:\n    branches: [main]\n", pr, true},
		"pull request skips another base branch":  {"on:\n  pull_request:\n    branches: [release]\n", pr, false},
		"pull request with no filter is admitted": {"on: pull_request\n", pr, true},
	} {
		t.Run(name, func(t *testing.T) {
			w := &Workspace{Ev: tc.ev}
			got, err := ghaAdmits([]byte(tc.file), w)
			if err != nil || got != tc.want {
				t.Fatalf("ghaAdmits = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestGhaAdmitsRefusesGlobFormsItCannotEvaluate(t *testing.T) {
	pr := &Workspace{Ev: Event{Kind: kindPR, Branch: "feature/x", Target: "main"}}
	push := &Workspace{Ev: Event{Kind: kindPush, Branch: "main"}}
	for name, tc := range map[string]struct {
		w    *Workspace
		file string
	}{
		"question mark in a pull request filter": {pr, "on:\n  pull_request:\n    branches: ['ma?n']\n"},
		"plus in a push filter":                  {push, "on:\n  push:\n    branches: ['ma+in']\n"},
		"negated pattern":                        {push, "on:\n  push:\n    branches: ['!main']\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := ghaAdmits([]byte(tc.file), tc.w); err == nil {
				t.Fatalf("ghaAdmits = %v, nil, want an error", got)
			}
		})
	}
}

func TestGhaWorkflows(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wf, 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"a.yml":  "on:\n  push:\n    branches: [main]\n",
		"b.yaml": "on:\n  push:\n    tags: ['v*']\n",
		"c.yml":  "on: pull_request\n",
	}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(wf, n), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w := &Workspace{Dir: dir, Ev: Event{Kind: kindPush, Branch: "main"}}
	got, total, err := ghaWorkflows(w)
	if err != nil || total != 3 || !reflect.DeepEqual(got, []string{filepath.Join(wf, "a.yml")}) {
		t.Fatalf("ghaWorkflows = %v, %d, %v", got, total, err)
	}
	if err := os.WriteFile(filepath.Join(wf, "d.yml"), []byte("on:\n  push:\n    branches: ['!x']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ghaWorkflows(w); err == nil {
		t.Fatal("ghaWorkflows accepted a filter it cannot evaluate")
	}
}
