package gha_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
)

func TestCachePolicyMapping(t *testing.T) {
	cases := map[string]struct {
		policy   string
		wantUses string
		atEnd    bool
		level    caps.Level
	}{
		"default":   {"", "actions/cache@", false, caps.Native},
		"pull-push": {"pull-push", "actions/cache@", false, caps.Native},
		"pull":      {"pull", "actions/cache/restore@", false, caps.Native},
		"push":      {"push", "actions/cache/save@", true, caps.Native},
		"unknown":   {"bogus", "actions/cache@", false, caps.Emulated},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := baseProject(t)
			job(p, "build").Cache = []ir.Cache{{Key: "deps-${{ sha }}", Policy: c.policy, Paths: []string{"~/.cache", "vendor"}}}
			files, rep, err := gha.New(gha.GHA).Emit(p)
			if err != nil {
				t.Fatal(err)
			}
			data := string(files[".github/workflows/ci.yml"])
			cacheAt := strings.Index(data, c.wantUses)
			runAt := strings.Index(data, "make build")
			if cacheAt < 0 || runAt < 0 {
				t.Fatalf("missing %q or the job script:\n%s", c.wantUses, data)
			}
			if (cacheAt > runAt) != c.atEnd {
				t.Errorf("cache step after the script = %v, want %v:\n%s", cacheAt > runAt, c.atEnd, data)
			}
			if !strings.Contains(data, "key: deps-${{ github.sha }}") || !strings.Contains(data, "~/.cache") {
				t.Errorf("key or paths missing:\n%s", data)
			}
			if got := entryLevel(rep, caps.JobCache, jobBuild); got != c.level {
				t.Errorf("level = %v, want %v", got, c.level)
			}
		})
	}
}

func TestCachePolicyRoundTrip(t *testing.T) {
	for _, policy := range []string{"", "pull", "push"} {
		t.Run("policy "+policy, func(t *testing.T) {
			p := baseProject(t)
			j := job(p, "build")
			steps := len(j.Steps)
			j.Cache = []ir.Cache{{Key: "k", Policy: policy, Paths: []string{"vendor"}}}
			back, text := viaGHA(t, p)
			bj := back.Pipelines[0].Jobs[0]
			if len(bj.Steps) != steps || len(bj.Cache) != 1 || bj.Cache[0].Policy != policy {
				t.Errorf("cache not restored: steps=%d cache=%+v\n%s", len(bj.Steps), bj.Cache, text)
			}
		})
	}
}

func TestCacheKeysRollAndRestore(t *testing.T) {
	cases := map[string]struct {
		cache   ir.Cache
		want    []string
		notWant []string
	}{
		"static key rolls": {
			ir.Cache{Key: "deps", Paths: []string{"vendor"}},
			[]string{"key: deps-${{ github.run_id }}", "restore-keys: deps-"}, nil,
		},
		"no key shares the GitLab default": {
			ir.Cache{Paths: []string{"vendor"}},
			[]string{"key: default-${{ github.run_id }}", "restore-keys: default-"}, []string{"build-cache"},
		},
		"content hash key stays": {
			ir.Cache{Key: "go-${{ hashFiles('go.sum') }}", Paths: []string{"vendor"}},
			[]string{"key: go-${{ hashFiles('go.sum') }}"}, []string{"run_id", "restore-keys"},
		},
		"fallback keys": {
			ir.Cache{Key: "deps-main", FallbackKeys: []string{"deps-default", "x-${{ hashFiles('a') }}"}, Paths: []string{"vendor"}},
			[]string{"key: deps-main-${{ github.run_id }}", "deps-main-", "deps-default-", "x-${{ hashFiles('a') }}"}, nil,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := baseProject(t)
			job(p, "build").Cache = []ir.Cache{c.cache}
			files, _, err := gha.New(gha.GHA).Emit(p)
			if err != nil {
				t.Fatal(err)
			}
			data := string(files[".github/workflows/ci.yml"])
			for _, w := range c.want {
				if !strings.Contains(data, w) {
					t.Errorf("want %q in\n%s", w, data)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(data, w) {
					t.Errorf("did not want %q in\n%s", w, data)
				}
			}
			lintFileIgnoring(t, "ci.yml", files[".github/workflows/ci.yml"], constantIf)
		})
	}
}

func TestSaveStepHasNoRestoreKeys(t *testing.T) {
	p := baseProject(t)
	job(p, "build").Cache = []ir.Cache{{Key: "deps", Policy: "push", FallbackKeys: []string{"old"}, Paths: []string{"vendor"}}}
	files, _, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	data := string(files[".github/workflows/ci.yml"])
	if strings.Contains(data, "restore-keys") || !strings.Contains(data, "key: deps-${{ github.run_id }}") {
		t.Errorf("save step is wrong:\n%s", data)
	}
	lintFileIgnoring(t, "ci.yml", files[".github/workflows/ci.yml"], constantIf)
}

func TestCacheFallbackKeysRoundTrip(t *testing.T) {
	p := baseProject(t)
	job(p, "build").Cache = []ir.Cache{{Key: "deps", FallbackKeys: []string{"a", "b"}, Paths: []string{"vendor"}}}
	back, text := viaGHA(t, p)
	c := back.Pipelines[0].Jobs[0].Cache
	if len(c) != 1 || c[0].Key != "deps" || !slices.Equal(c[0].FallbackKeys, []string{"a", "b"}) || !slices.Equal(c[0].Paths, []string{"vendor"}) {
		t.Errorf("cache = %+v\n%s", c, text)
	}
}
