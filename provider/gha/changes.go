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

// globERE converts a GitLab glob (Ruby File.fnmatch with FNM_PATHNAME, FNM_DOTMATCH and FNM_EXTGLOB) into an
// extended regular expression over whole paths. ok is false for a glob the converter does not model.
func globERE(g string) (string, bool) {
	var sb strings.Builder
	braces := 0
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case c >= 0x80 || c == '\'' || c == '\\' || c == '\n':
			return "", false
		case c == '*' && strings.HasPrefix(g[i:], "**/") && (i == 0 || g[i-1] == '/'):
			sb.WriteString("(.*/)?")
			i += 2
		case c == '*':
			for i+1 < len(g) && g[i+1] == '*' {
				i++
			}
			sb.WriteString("[^/]*")
		case c == '?':
			sb.WriteString("[^/]")
		case c == '{':
			braces++
			sb.WriteString("(")
		case c == '}' && braces > 0:
			braces--
			sb.WriteString(")")
		case c == ',' && braces > 0:
			sb.WriteString("|")
		case c == '[':
			j := strings.IndexByte(g[i:], ']')
			if j < 2 {
				return "", false
			}
			cls := g[i+1 : i+j]
			if strings.HasPrefix(cls, "!") {
				cls = "^" + cls[1:]
			}
			if strings.ContainsAny(cls, "[]") || strings.Contains(cls, "[:") {
				return "", false
			}
			sb.WriteString("[" + cls + "]")
			i += j
		case strings.IndexByte(`.+()|^$}`, c) >= 0:
			sb.WriteString(`\` + string(c))
		default:
			sb.WriteByte(c)
		}
	}
	if braces != 0 {
		return "", false
	}
	return sb.String(), true
}

func atomERE(globs []string) (string, bool) {
	parts := make([]string, len(globs))
	for i, g := range globs {
		re, ok := globERE(g)
		if !ok {
			return "", false
		}
		parts[i] = re
	}
	return "^(" + strings.Join(parts, "|") + ")$", true
}

// changesScript computes the verdict of each atom, as GitLab's rules:changes does: a push compares with the
// commit before it, a merge request with the target branch, and a tag, a new branch, a schedule or a manual
// run counts every path as changed. A diff that cannot be computed fails the job, so no gated job runs.
func changesScript(atoms []changeAtom) (string, bool) {
	var sb strings.Builder
	sb.WriteString(`set -eu
files="$(mktemp)"
all=false
case "$EVENT" in
  push)
    if [ "$REF_TYPE" = tag ] || [ "$BEFORE" = ` + zeroSHA + ` ]; then
      all=true
    else
      git -c core.quotepath=off diff --name-only --no-renames "$BEFORE" "$AFTER" > "$files"
    fi ;;
  pull_request)
    git -c core.quotepath=off diff --name-only --no-renames "$BASE...$HEAD" > "$files" ;;
  *)
    all=true ;;
esac
emit() {
  if [ "$all" = true ] || grep -Eq -e "$2" "$files"; then
    echo "$1=true" >> "$GITHUB_OUTPUT"
  else
    echo "$1=false" >> "$GITHUB_OUTPUT"
  fi
}
`)
	for _, a := range atoms {
		re, ok := atomERE(a.globs)
		if !ok {
			return "", false
		}
		sb.WriteString("emit " + a.out + " '" + re + "'\n")
	}
	return sb.String(), true
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
	detail := "rules:changes is computed by the generated job " + changesJobID + ": a push compares with the commit before it, a merge request with the target branch, and a tag, a new branch or a schedule counts every path as changed"
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
	script, ok := changesScript(e.changeAtoms)
	if !ok {
		return nil, fmt.Errorf("a path glob has no regular expression")
	}
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
