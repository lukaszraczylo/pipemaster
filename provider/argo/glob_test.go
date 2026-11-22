package argo_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func headsRegex(t *testing.T, sensor string) *regexp.Regexp {
	t.Helper()
	for _, l := range strings.Split(sensor, "\n") {
		if v := strings.Trim(strings.TrimSpace(l), "-' \""); strings.HasPrefix(v, "^refs/heads/") {
			return regexp.MustCompile(v)
		}
	}
	t.Fatalf("no branch regex in:\n%s", sensor)
	return nil
}

// The IR meaning is the Tekton one: * stays inside one path segment, ** crosses segments.
func TestBranchGlobStarStaysInOneSegment(t *testing.T) {
	cases := []struct {
		glob        string
		match, miss []string
	}{
		{"release/*", []string{"release/1", "release/"}, []string{"release/a/b", "xrelease/1"}},
		{"feature/**", []string{"feature/a", "feature/a/b"}, []string{"other/a"}},
		{"a/**/b", []string{"a/b", "a/x/b", "a/x/y/b"}, []string{"a/xb"}},
		{"v?", []string{"v1"}, []string{"v12", "v/"}},
		{"main", []string{"main"}, []string{"main2", "xmain"}},
	}
	for _, c := range cases {
		t.Run(c.glob, func(t *testing.T) {
			files, _ := emit(t, triggerProject(ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{c.glob}}}))
			re := headsRegex(t, files[".argo/events/sensor.yaml"])
			for _, m := range c.match {
				if !re.MatchString("refs/heads/" + m) {
					t.Errorf("%s: %s should match %q", c.glob, re, m)
				}
			}
			for _, m := range c.miss {
				if re.MatchString("refs/heads/" + m) {
					t.Errorf("%s: %s must not match %q", c.glob, re, m)
				}
			}
			back := roundTrip(t, files)
			if got := back.Pipelines[0].Triggers[0].Branches.Include; len(got) != 1 || got[0] != c.glob {
				t.Errorf("read back %v, want [%s]", got, c.glob)
			}
		})
	}
}

func TestSensorRegexReadsBackWithTheSameMeaning(t *testing.T) {
	cases := map[string]string{
		`^refs/heads/feature/.*$`:    "feature/**",
		`^refs/heads/feature/[^/]*$`: "feature/*",
		`^refs/heads/main$`:          "main",
		`^refs/heads/rel\.[^/]*$`:    "rel.*",
	}
	for re, want := range cases {
		t.Run(re, func(t *testing.T) {
			filters := "      - {path: headers.X-Github-Event, type: string, value: ['push']}\n      - {path: body.ref, type: string, value: ['" + re + "']}\n"
			trs, _ := triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor(dep(filters), runCI)})
			if len(trs) != 1 || len(trs[0].Branches.Include) != 1 || trs[0].Branches.Include[0] != want {
				t.Errorf("%s read as %+v, want [%s]", re, trs, want)
			}
		})
	}
}

func TestRegexWithNoGlobFormStaysInExt(t *testing.T) {
	for _, re := range []string{`^refs/heads/a.*/b$`, `^refs/heads/a/.*/b$`, `^refs/heads/a[^/]+$`, `^refs/heads/.*/x$`} {
		t.Run(re, func(t *testing.T) {
			filters := "      - {path: headers.X-Github-Event, type: string, value: ['push']}\n      - {path: body.ref, type: string, value: ['" + re + "']}\n"
			trs, rep := triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor(dep(filters), runCI)})
			for _, tr := range trs {
				if len(tr.Branches.Include) > 0 {
					t.Errorf("%s was read as the glob %v", re, tr.Branches.Include)
				}
			}
			if rep == "" {
				t.Errorf("%s: no report entry", re)
			}
		})
	}
}

func TestTagWithoutFilterStillReadsAsNoFilter(t *testing.T) {
	files, _ := emit(t, triggerProject(ir.Trigger{Kind: ir.TriggerTag}))
	back := roundTrip(t, files)
	if got := back.Pipelines[0].Triggers[0].Tags.Include; len(got) != 0 {
		t.Errorf("tag all read back as %v", got)
	}
	if !strings.Contains(files[".argo/events/sensor.yaml"], "refs/tags/") {
		t.Error("no tag filter written")
	}
}

// Every glob over a small alphabet must survive emit and parse with its text unchanged.
func TestBranchGlobRoundTripsOverASmallAlphabet(t *testing.T) {
	const alphabet = "a./*?"
	var globs []string
	var gen func(prefix string, n int)
	gen = func(prefix string, n int) {
		if prefix != "" {
			globs = append(globs, prefix)
		}
		if n == 0 {
			return
		}
		for _, c := range alphabet {
			gen(prefix+string(c), n-1)
		}
	}
	gen("", 4)
	for _, g := range globs {
		files, _ := emit(t, triggerProject(ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{g}}}))
		back := roundTrip(t, files)
		if got := back.Pipelines[0].Triggers[0].Branches.Include; len(got) != 1 || got[0] != g {
			t.Fatalf("glob %q read back as %v\n%s", g, got, files[".argo/events/sensor.yaml"])
		}
	}
}
