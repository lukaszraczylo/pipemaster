package drone

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// ChangesDir holds one file per path predicate in the workspace of a Drone pipeline; the generated changes step
// writes it and the guard of a later step reads it. It sits in .git so the build context and the work tree stay clean.
const ChangesDir = ".git/pipemaster-changes"

// ChangesResultCall is the node ParseGuard reads for a path predicate guard: the predicate globs are not in the
// text, so the node names the result file and the guard marker restores the predicate.
const ChangesResultCall = "changes_result"

const (
	changesTestHead = `[ "$(cat ` + ChangesDir + `/`
	changesTestTail = ` 2>/dev/null)" = 'true' ]`
)

// changesOutOf names the result file of a path predicate or of its result node.
func changesOutOf(c expr.Call) (string, bool) {
	if _, ok := expr.ChangesGlobs(c); ok {
		return expr.ChangesOut(c), true
	}
	if c.Name == ChangesResultCall && len(c.Args) == 1 {
		if l, ok := c.Args[0].(expr.Lit); ok && l.Kind == expr.LitStr {
			return l.Str, true
		}
	}
	return "", false
}

// atomText prints a path predicate as the test of its result file: a missing file is not true.
func atomText(c expr.Call) (string, bool) {
	out, ok := changesOutOf(c)
	if !ok || !validOut(out) {
		return "", false
	}
	return changesTestHead + out + changesTestTail, true
}

// validOut accepts the names expr.ChangesOut writes.
func validOut(s string) bool {
	if len(s) != 9 || s[0] != 'c' {
		return false
	}
	for _, r := range s[1:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func atomRead(s string) (expr.Expr, int, bool) {
	rest, ok := strings.CutPrefix(s, changesTestHead)
	if !ok || len(rest) < 9 || !validOut(rest[:9]) || !strings.HasPrefix(rest[9:], changesTestTail) {
		return nil, 0, false
	}
	return expr.Call{Name: ChangesResultCall, Args: []expr.Expr{expr.Lit{Kind: expr.LitStr, Str: rest[:9]}}},
		len(changesTestHead) + 9 + len(changesTestTail), true
}
