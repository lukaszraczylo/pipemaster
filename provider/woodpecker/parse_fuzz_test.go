package woodpecker_test

import (
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/woodpecker"
)

func FuzzParse(f *testing.F) {
	f.Add(minimal)
	f.Add("when:\n  - event: push\n    branch: [main, 'release/*']\n  - event: tag\nmatrix:\n  A: [1, 2]\nsteps:\n  s:\n    image: x\n    commands: [echo ${A} $${B}]\n")
	f.Add("x: &a {image: y}\nsteps:\n  - name: s\n    <<: *a\n    settings: {k: [1]}\n")
	f.Fuzz(func(t *testing.T, src string) {
		fsys := fstest.MapFS{".woodpecker.yaml": &fstest.MapFile{Data: []byte(src)}}
		p, _, err := woodpecker.New().Parse(fsys)
		if err != nil {
			return
		}
		if err := ir.Validate(p); err != nil {
			t.Fatalf("Parse returned an invalid project: %v", err)
		}
		if _, err := ir.Marshal(p); err != nil {
			t.Fatalf("Marshal: %v", err)
		}
	})
}
