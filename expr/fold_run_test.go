package expr_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
)

func TestFoldRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
		tag  bool
	}{
		{"event true on push", "event == 'push'", "true", false},
		{"event false on tag", "event == 'push'", "false", true},
		{"or keeps the branch test", "event == 'tag' || branch == 'main'", "branch == 'main'", false},
		{"or is true on a tag", "event == 'tag' || branch == 'main'", "true", true},
		{"branch ref becomes the branch", "ref == 'refs/heads/main'", "branch == 'main'", false},
		{"branch ref is false on a tag", "ref == 'refs/heads/main'", "false", true},
		{"tag ref becomes the tag", "ref == 'refs/tags/v1'", "tag == 'v1'", true},
		{"tag prefix on a tag", "startsWith(ref, 'refs/tags/')", "true", true},
		{"tag prefix on a push", "startsWith(ref, 'refs/tags/')", "false", false},
		{"partial prefix keeps the name test", "startsWith(ref, 'refs/heads/rel')", "startsWith(branch, 'rel')", false},
		{"not equal ref", "ref != 'refs/heads/main'", "!(branch == 'main')", false},
		{"ref type", "ref_type == 'tag'", "true", true},
		{"unknown stays", "actor == 'bob'", "actor == 'bob'", false},
		{"and with unknown", "event == 'push' && actor == 'bob'", "actor == 'bob'", false},
		{"and never", "event == 'tag' && actor == 'bob'", "false", false},
		{"pull request value is null", "pr.number == '1'", "false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := expr.ParseCanonical(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := expr.String(expr.FoldRun(in, tc.tag)); got != tc.want {
				t.Fatalf("FoldRun(%s, tag=%v) = %s, want %s", tc.in, tc.tag, got, tc.want)
			}
		})
	}
}
