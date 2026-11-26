//go:build behave

package behave

import "testing"

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
