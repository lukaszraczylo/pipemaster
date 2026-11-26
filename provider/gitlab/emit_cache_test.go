package gitlab_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/validate"
)

func TestJobCacheIsNativeWithPolicy(t *testing.T) {
	for _, policy := range []string{"", "pull", "push", "pull-push"} {
		t.Run("policy "+policy, func(t *testing.T) {
			build := glJob("build", glStep("make"))
			build.Cache = []ir.Cache{{Key: "deps", Policy: policy, Paths: []string{"vendor"}}}
			files, rep := emitFiles(t, glProject("gha", build))
			doc := string(files[rootFile])
			for _, want := range []string{"cache:", "key: deps", "- vendor"} {
				if !strings.Contains(doc, want) {
					t.Errorf("want %q in\n%s", want, doc)
				}
			}
			if got := strings.Contains(doc, "policy: "+policy); got != (policy != "") {
				t.Errorf("policy %q written = %v\n%s", policy, got, doc)
			}
			for _, e := range rep.Entries {
				if e.Feature == caps.JobCache {
					t.Errorf("cache has a non-native entry: %+v", e)
				}
			}
			if err := validate.File("gitlab", rootFile, []byte(doc)); err != nil {
				t.Errorf("%v\n%s", err, doc)
			}
		})
	}
}

func TestCacheFallbackKeysAreCarried(t *testing.T) {
	pl, _ := pipeline(t, "j:\n  script: [x]\n  cache:\n    key: deps-main\n    fallback_keys: [deps-default, deps-old]\n    paths: [vendor]\n")
	j := job(t, pl, "j")
	if len(j.Cache) != 1 || !slices.Equal(j.Cache[0].FallbackKeys, []string{"deps-default", "deps-old"}) {
		t.Fatalf("cache = %+v", j.Cache)
	}
	if x, ok := ext(j.Ext)["cache"]; ok {
		t.Errorf("fallback_keys still in Ext: %v", x)
	}
	p := glProject("gha", glJob("build", glStep("make")))
	p.Pipelines[0].Jobs[0].Cache = j.Cache
	doc := emitDoc(t, p)
	if !strings.Contains(doc, "fallback_keys:") || !strings.Contains(doc, "- deps-default") || !strings.Contains(doc, "- deps-old") {
		t.Errorf("fallback_keys not emitted:\n%s", doc)
	}
	if err := validate.File("gitlab", rootFile, []byte(doc)); err != nil {
		t.Errorf("%v\n%s", err, doc)
	}
}
