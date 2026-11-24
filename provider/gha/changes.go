package gha

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// changesJobID names the generated job that computes which atoms of a path predicate hold.
const changesJobID = "pipemaster-changes"

const (
	changesStep    = "changes"
	changesTrue    = "true"
	changesFalse   = "false"
	zeroSHA        = "0000000000000000000000000000000000000000"
	maxChangesAtom = 4
)

// changeAtom is one path predicate with the output that carries its verdict.
type changeAtom struct {
	out   string
	globs []string
}

func atomOut(a expr.Expr) string {
	sum := sha256.Sum256([]byte(expr.String(a)))
	return "c" + hex.EncodeToString(sum[:4])
}

// changesVerdict is the value a changes output must have.
func changesLit(out string, want string) expr.Expr {
	return expr.Opaque{Dialect: exprgha.Dialect, Text: fmt.Sprintf("needs.%s.outputs.%s == '%s'", changesJobID, out, want)}
}

type shan struct {
	x    expr.Expr
	kind byte
}

// translateChanges rewrites a condition with path predicates into one over the outputs of the generated job.
// It expands the condition over the predicates (at most maxChangesAtom), translates each changes-free branch
// with the usual translator, and joins the branches. ok is false when any branch has no GHA form.
func translateChanges(x expr.Expr, from expr.Dialect) (expr.Expr, []string, []changeAtom, bool) {
	atoms := expr.ChangesAtoms(x)
	if len(atoms) == 0 || len(atoms) > maxChangesAtom {
		return nil, nil, nil, false
	}
	var cav []string
	var used []changeAtom
	for _, a := range atoms {
		g, ok := expr.ChangesGlobs(a)
		if !ok {
			return nil, nil, nil, false
		}
		if _, ok := atomERE(g); !ok {
			return nil, nil, nil, false
		}
		used = append(used, changeAtom{out: atomOut(a), globs: g})
	}
	var rec func(e expr.Expr) (shan, bool)
	rec = func(e expr.Expr) (shan, bool) {
		rest := expr.ChangesAtoms(e)
		if len(rest) == 0 {
			if l, ok := e.(expr.Lit); ok && l.Kind == expr.LitBool {
				if l.Bool {
					return shan{kind: 't'}, true
				}
				return shan{kind: 'f'}, true
			}
			t, c, ok := expr.TranslateApprox(e, from, exprgha.Lang)
			if !ok {
				return shan{}, false
			}
			cav = append(cav, c...)
			return shan{kind: 'x', x: t}, true
		}
		a := rest[0]
		key := expr.String(a)
		yes, ok := rec(expr.AssignChanges(e, map[string]bool{key: true}))
		if !ok {
			return shan{}, false
		}
		no, ok := rec(expr.AssignChanges(e, map[string]bool{key: false}))
		if !ok {
			return shan{}, false
		}
		if yes.kind == no.kind && (yes.kind != 'x' || expr.String(yes.x) == expr.String(no.x)) {
			return yes, true
		}
		out := atomOut(a)
		return orShan(andShan(changesLit(out, changesTrue), yes), andShan(changesLit(out, changesFalse), no)), true
	}
	r, ok := rec(x)
	if !ok || r.kind == 'f' {
		return nil, nil, nil, false
	}
	if r.kind == 't' {
		return nil, nil, nil, false
	}
	return r.x, cav, used, true
}

// andShan conjoins a literal with a branch; the literal alone stands for an always-true branch.
func andShan(lit expr.Expr, b shan) shan {
	switch b.kind {
	case 'f':
		return b
	case 't':
		return shan{kind: 'x', x: lit}
	}
	return shan{kind: 'x', x: expr.Binary{Op: expr.OpAnd, L: lit, R: b.x}}
}

func orShan(a, b shan) shan {
	switch {
	case a.kind == 'f':
		return b
	case b.kind == 'f':
		return a
	}
	return shan{kind: 'x', x: expr.Binary{Op: expr.OpOr, L: a.x, R: b.x}}
}

// The glob to regular expression converters live in emitkit, shared with the Tekton and Argo emitters.
var (
	globERE  = emitkit.GlobERE
	globByte = emitkit.GlobByte
	classERE = emitkit.ClassERE
	atomERE  = emitkit.AtomERE
)

// changesScript computes the verdict of each atom, as GitLab's rules:changes does: a push compares with the
// commit before it, a merge request with the target branch, and a tag, a new branch, a schedule or a manual
// run counts every path as changed. A diff that cannot be computed fails the job with a message, so no gated
// job runs. The regexes come in as RE_<output> environment variables and never enter the script text. Paths are
// NUL-separated and matched one by one as whole strings, so a name with a quote, a backslash or a newline matches as it is.
func changesScript(atoms []changeAtom) string {
	var sb strings.Builder
	sb.WriteString(`set -eu
files="$(mktemp)"
all=false
diff_paths() {
  git -c core.quotepath=off diff -z --name-only --no-renames "$@" > "$files" ||
    { echo "pipemaster-changes: cannot compute the changed paths for $*" >&2; exit 1; }
}
case "$EVENT" in
  push)
    if [ "$REF_TYPE" = tag ] || [ "$BEFORE" = ` + zeroSHA + ` ]; then
      all=true
    else
      if ! git cat-file -e "$BEFORE^{commit}" 2>/dev/null; then
        git fetch --no-tags --quiet origin "$BEFORE" 2>/dev/null || true
      fi
      if ! git cat-file -e "$BEFORE^{commit}" 2>/dev/null; then
        echo "pipemaster-changes: the commit before the push ($BEFORE) is not in the clone, as after a force push; GitLab documents no verdict for this" >&2
        exit 1
      fi
      diff_paths "$BEFORE" "$AFTER"
    fi ;;
  pull_request)
    diff_paths "$BASE...$HEAD" ;;
  *)
    all=true ;;
esac
emit() {
  # Each NUL-separated name is matched as a whole string: bash [[ =~ ]] anchors at the string ends, where grep
  # would also anchor at a newline inside a name.
  hit=false
  if [ "$all" = true ]; then
    hit=true
  else
    while IFS= read -r -d '' name; do
      if [[ $name =~ $2 ]]; then hit=true; break; fi
    done < "$files"
  fi
  echo "$1=$hit" >> "$GITHUB_OUTPUT"
}
`)
	for _, a := range atoms {
		sb.WriteString("emit " + a.out + ` "$RE_` + a.out + `"` + "\n")
	}
	return sb.String()
}

// sourceChanges translates a condition with path predicates and records what the generated job must compute.
func (e *emitter) sourceChanges(path string, f caps.Feature, x expr.Expr) (expr.Expr, bool) {
	if e.changesBlocked {
		return nil, false
	}
	t, cav, atoms, ok := translateChanges(x, *e.from)
	if !ok {
		return nil, false
	}
	for _, a := range atoms {
		if !slices.ContainsFunc(e.changeAtoms, func(b changeAtom) bool { return b.out == a.out }) {
			e.changeAtoms = append(e.changeAtoms, a)
		}
	}
	e.changesNeeded = true
	detail := "rules:changes is computed by the generated job " + changesJobID + ": a push compares with the commit before it, a merge request with the target branch, and a tag, a new branch, a schedule or any other event counts every path as changed; a commit before the push that the clone cannot fetch, as after a force push, fails the job"
	if len(cav) > 0 {
		detail += "; " + strings.Join(cav, "; ")
	}
	e.emulate(path, f, "changed-paths job "+changesJobID, detail)
	if f == caps.JobIf || f == caps.StepIf {
		e.markSource(f, path, x)
	}
	return t, true
}

// changesJob writes the generated job that holds the verdict of each path predicate.
func (e *emitter) changesJob(pl *ir.Pipeline) (*yaml.Node, error) {
	jp := jobPath(pl.ID, changesJobID)
	script := changesScript(e.changeAtoms)
	ref, err := reverseAction("checkout", "")
	if err != nil {
		return nil, err
	}
	uses, err := e.synthUses(jp, "checkout", ref)
	if err != nil {
		return nil, err
	}
	guard, err := printExpr(deletedGuard())
	if err != nil {
		return nil, err
	}
	job := newMap()
	job.set("if", str(guard))
	job.set("runs-on", str(defaultRunner))
	outs := newMap()
	for _, a := range e.changeAtoms {
		outs.set(a.out, str("${{ steps."+changesStep+".outputs."+a.out+" }}"))
	}
	job.set("outputs", outs.n)
	co := newMap()
	co.set("uses", str(uses))
	with := newMap()
	with.set("fetch-depth", intNode(0))
	co.set("with", with.n)
	run := newMap()
	run.set("id", str(changesStep))
	env := newMap()
	for _, kv := range [][2]string{
		{"EVENT", "github.event_name"}, {"REF_TYPE", "github.ref_type"}, {"BEFORE", "github.event.before"},
		{"AFTER", "github.sha"}, {"BASE", "github.event.pull_request.base.sha"}, {"HEAD", "github.event.pull_request.head.sha"},
	} {
		env.set(kv[0], str("${{ "+kv[1]+" }}"))
	}
	for _, a := range e.changeAtoms {
		re, ok := atomERE(a.globs)
		if !ok {
			return nil, fmt.Errorf("a path glob has no regular expression")
		}
		env.set("RE_"+a.out, str(re))
	}
	run.set("env", env.n)
	run.set("run", block([]string{strings.TrimRight(script, "\n")}))
	job.set("steps", seqOf(co.n, run.n))
	e.mark(caps.StepScript, jp)
	e.emulate(jp, caps.StepScript, "generated job "+changesJobID, "computes the paths each rules:changes list names")
	return job.n, nil
}

// needChanges adds the generated job to the needs of the job node m, before its if.
func needChanges(m *omap) {
	for i := 0; i+1 < len(m.n.Content); i += 2 {
		if m.n.Content[i].Value == "needs" {
			seq := m.n.Content[i+1]
			if seq.Kind == yaml.ScalarNode {
				seq = strSeq([]string{seq.Value})
				m.n.Content[i+1] = seq
			}
			seq.Content = append(seq.Content, str(changesJobID))
			return
		}
	}
	needs := strSeq([]string{changesJobID})
	for i := 0; i+1 < len(m.n.Content); i += 2 {
		if m.n.Content[i].Value == "if" {
			m.n.Content = append(m.n.Content[:i], append([]*yaml.Node{str("needs"), needs}, m.n.Content[i:]...)...)
			m.keys["needs"] = true
			return
		}
	}
	m.set("needs", needs)
}
