package gitlab_test

import (
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
