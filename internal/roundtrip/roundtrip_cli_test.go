package roundtrip_test

import (
	"path/filepath"
	"testing"
)

func TestRoundtripCommandOnConcourseCorpus(t *testing.T) {
	dirs, err := filepath.Glob("../../testdata/corpus/8[0-6]-concourse-*")
	if err != nil || len(dirs) != 7 {
		t.Fatalf("corpus dirs = %v, %v", dirs, err)
	}
	for _, dir := range dirs {
		for _, via := range []string{"concourse", "gha"} {
			t.Run(filepath.Base(dir)+"/via-"+via, func(t *testing.T) {
				t.Parallel()
				code, out, msg := runOut("roundtrip", dir, "--via", via)
				if code != 0 {
					t.Fatalf("exit %d\n%.2000s\n%.1000s", code, out, msg)
				}
			})
		}
	}
}
