package drone

import (
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	dexpr "github.com/lukaszraczylo/pipemaster/expr/drone"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// changesStepName names the generated step that computes the path predicates of the job condition.
const changesStepName = "pipemaster-changes"

// changesScriptPath lies in the build workspace: a fixed path under /tmp is shared by concurrent builds on an exec or ssh runner.
const changesScriptPath = dexpr.ChangesDir + "/changes.sh"

// changesPrelude maps the Drone build to the values the shared script reads. A tag, a schedule and every other
// event count every path as changed; a pull request compares with the fetched target branch.
const changesPrelude = `case "$DRONE_BUILD_EVENT" in
  push) EVENT=push; REF_TYPE=branch ;;
  tag) EVENT=push; REF_TYPE=tag ;;
  pull_request) EVENT=pull_request; REF_TYPE=branch ;;
  *) EVENT=other; REF_TYPE=branch ;;
esac
BEFORE="${DRONE_COMMIT_BEFORE:-}"
AFTER="${DRONE_COMMIT_AFTER:-}"
HEAD="$AFTER"
BASE=
if [ "$EVENT" = pull_request ]; then
  case "${DRONE_TARGET_BRANCH:-}" in ""|-*) echo "pipemaster-changes: the pull request names no usable target branch" >&2; exit 1 ;; esac
  git fetch --no-tags --quiet origin --end-of-options "$DRONE_TARGET_BRANCH" ||
    { echo "pipemaster-changes: cannot fetch the target branch $DRONE_TARGET_BRANCH" >&2; exit 1; }
  BASE="$(git rev-parse --verify FETCH_HEAD)"
fi
export EVENT REF_TYPE BEFORE AFTER HEAD BASE`

const changesDetail = "rules:changes is computed by the generated step " + changesStepName + ", which writes one result file for each path predicate under " + dexpr.ChangesDir +
	" and runs first in the document; every later script step starts with a guard that reads the results, so the step shows success instead of skipped. " +
	"A push compares with DRONE_COMMIT_BEFORE, a pull request with the target branch, and a tag, a schedule or any other event counts every path as changed. " +
	"The step runs in the pinned image " + emitkit.ChangesImage + " and needs the clone to hold the commits or to fetch them from origin with the credentials of the pipeline; " +
	"a commit that cannot be fetched fails the build. The step has not run on a Drone server"

// changesAtoms lists the path predicates that the job guard and the step guards of a document read.
func changesAtoms(guard expr.Expr, steps []*ir.Step) []expr.Expr {
	var out []expr.Expr
	add := func(x expr.Expr) {
		if x == nil {
			return
		}
		for _, a := range expr.ChangesAtoms(x) {
			if !slices.ContainsFunc(out, func(b expr.Expr) bool { return expr.String(a) == expr.String(b) }) {
				out = append(out, a)
			}
		}
	}
	add(guard)
	for _, st := range steps {
		if st != nil {
			add(st.If)
		}
	}
	return out
}

// hasStepDeps is true when a step names its dependencies: a generated first step would not gate them.
func hasStepDeps(steps []*ir.Step) bool {
	return slices.ContainsFunc(steps, func(st *ir.Step) bool {
		if st == nil {
			return false
		}
		_, has := extOf(st.Ext)[keyDependsOn]
		return has
	})
}

// changesStep writes the generated step; it computes every predicate in one run.
func (e *emitter) changesStep(atoms []expr.Expr) *yaml.Node {
	plans := make([]emitkit.ChangesPlan, 0, len(atoms))
	env := newYMap()
	for _, a := range atoms {
		globs, _ := expr.ChangesGlobs(a)
		out := expr.ChangesOut(a)
		plans = append(plans, emitkit.ChangesPlan{Out: out, Terms: [][]bool{{true}}, Atoms: []emitkit.ChangeAtom{{Out: out, Globs: globs}}})
		re, _ := emitkit.AtomERE(globs)
		env.set("RE_"+out, ystr(esc(re)))
	}
	body := emitkit.ChangesScript(plans, false, func(out string) string { return `"` + dexpr.ChangesDir + "/" + out + `"` })
	script := strings.Join([]string{
		"set -eu",
		"mkdir -p " + dexpr.ChangesDir,
		"cat > " + changesScriptPath + " <<'PIPEMASTER_CHANGES_EOF'",
		strings.TrimRight(body, "\n"),
		"PIPEMASTER_CHANGES_EOF",
		changesPrelude,
		"exec bash " + changesScriptPath,
	}, "\n")
	m := newYMap()
	m.set(keyName, ystr(changesStepName))
	m.set(keyImage, ystr(emitkit.ChangesImage))
	m.set(keyEnv, env.n)
	m.set(keyCommands, yseq(block(esc(script))))
	return m.n
}
