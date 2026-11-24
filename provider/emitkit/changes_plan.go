package emitkit

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// MaxChangesAtom is the most path predicates one condition may hold, as the GitHub Actions emitter allows.
const MaxChangesAtom = 4

// ChangesZeroSHA is the commit a push that creates a ref replaces.
const ChangesZeroSHA = "0000000000000000000000000000000000000000"

// ChangeAtom is one path predicate with the name of the shell value that holds its verdict.
type ChangeAtom struct {
	Out   string
	Globs []string
}

// ChangesPlan splits a condition that holds path predicates into what the changed paths decide and what they do not.
// The condition holds exactly when the Terms formula over the predicates holds and the Residual holds.
type ChangesPlan struct {
	Residual expr.Expr
	// Out names the result that carries the formula verdict: "true" or "false".
	Out   string
	Atoms []ChangeAtom
	Terms [][]bool
}

// ChangesAtomOut names the shell value of a path predicate.
func ChangesAtomOut(a expr.Expr) string {
	sum := sha256.Sum256([]byte(expr.String(a)))
	return "c" + hex.EncodeToString(sum[:4])
}

// DecomposeChanges splits x into a plan. ok is false when x has no path predicate, has more than MaxChangesAtom,
// holds a glob that has no regular expression, or needs two different residual conditions under different
// verdicts of the predicates: one result and one residual cannot state that.
func DecomposeChanges(x expr.Expr) (ChangesPlan, bool) {
	atoms := expr.ChangesAtoms(x)
	if len(atoms) == 0 || len(atoms) > MaxChangesAtom {
		return ChangesPlan{}, false
	}
	p := ChangesPlan{}
	for _, a := range atoms {
		g, ok := expr.ChangesGlobs(a)
		if !ok {
			return ChangesPlan{}, false
		}
		if _, ok := AtomERE(g); !ok {
			return ChangesPlan{}, false
		}
		p.Atoms = append(p.Atoms, ChangeAtom{Out: ChangesAtomOut(a), Globs: g})
	}
	var residual expr.Expr
	haveResidual, haveTrue := false, false
	for mask := 0; mask < 1<<len(atoms); mask++ {
		vals := map[string]bool{}
		term := make([]bool, len(atoms))
		for i, a := range atoms {
			term[i] = mask&(1<<i) != 0
			vals[expr.String(a)] = term[i]
		}
		r := expr.AssignChanges(x, vals)
		if l, ok := r.(expr.Lit); ok && l.Kind == expr.LitBool {
			if !l.Bool {
				continue
			}
			haveTrue = true
		} else if !haveResidual {
			residual, haveResidual = r, true
		} else if expr.String(r) != expr.String(residual) {
			return ChangesPlan{}, false
		}
		p.Terms = append(p.Terms, term)
	}
	if len(p.Terms) == 0 || haveTrue && haveResidual || len(p.Terms) == 1<<len(atoms) && !haveResidual {
		return ChangesPlan{}, false
	}
	p.Residual = residual
	sum := sha256.Sum256([]byte(expr.String(x)))
	p.Out = "s" + hex.EncodeToString(sum[:4])
	return p, true
}

// ChangesImage runs the changes script on Tekton and Argo: it holds bash and git, so nothing installs at run time.
// Tag trixie-scm (git 2.47, bash 5.2); the multi-arch index digest was read with docker pull on 2026-11-24.
const ChangesImage = "buildpack-deps:trixie-scm@sha256:d524aa66d91264606eddea5b94af462fc3b969b84a1d4083e537a9ab84b579c5"

// ChangesAtomsOf lists the distinct atoms of several plans in first-seen order.
func ChangesAtomsOf(plans []ChangesPlan) []ChangeAtom {
	var out []ChangeAtom
	for _, p := range plans {
		for _, a := range p.Atoms {
			if !slices.ContainsFunc(out, func(b ChangeAtom) bool { return b.Out == a.Out }) {
				out = append(out, a)
			}
		}
	}
	return out
}

// ChangesScript computes the verdict of each path predicate, as GitLab's rules:changes does, and writes the verdict
// of each plan to the place dest names. A push compares with the commit before it, a merge request with the target
// branch, and a tag, a new branch, a schedule or a manual run counts every path as changed. A diff that cannot be
// computed fails the step with a message, so no gated job runs. The regexes come in as RE_<out> environment variables
// and never enter the script text. Paths are NUL-separated and matched one by one as whole strings. The script
// expects git and bash, and the clone in the current directory unless fetch is true, which makes it fetch the commits.
func ChangesScript(plans []ChangesPlan, fetch bool, dest func(out string) string) string {
	var sb strings.Builder
	sb.WriteString(`set -eu
files="$(mktemp)"
all=false
for rev in "${BEFORE:-}" "${AFTER:-}" "${BASE:-}" "${HEAD:-}"; do
  case "$rev" in -*) echo "pipemaster-changes: refusing the commit value $rev, which starts with a dash" >&2; exit 1 ;; esac
done
diff_paths() {
  git -c core.quotepath=off diff -z --name-only --no-renames --end-of-options "$@" > "$files" ||
    { echo "pipemaster-changes: cannot compute the changed paths for $*" >&2; exit 1; }
}
`)
	if fetch {
		sb.WriteString(`fetch_commits() {
  git init -q .
  git remote add origin "${PIPEMASTER_CHECKOUT_HOST:-https://github.com}/$REPO.git"
  git fetch --no-tags --quiet origin --end-of-options "$@" || echo "pipemaster-changes: git fetch failed for $*" >&2
}
`)
	}
	sb.WriteString(`case "$EVENT" in
  push)
    if [ "$REF_TYPE" = tag ] || [ "$BEFORE" = ` + ChangesZeroSHA + ` ]; then
      all=true
    else
`)
	if fetch {
		sb.WriteString("      fetch_commits \"$AFTER\" \"$BEFORE\"\n")
	}
	sb.WriteString(`      if ! git cat-file -e "$BEFORE^{commit}" 2>/dev/null; then
        git fetch --no-tags --quiet origin --end-of-options "$BEFORE" || echo "pipemaster-changes: git fetch failed for $BEFORE" >&2
      fi
      if ! git cat-file -e "$BEFORE^{commit}" 2>/dev/null; then
        echo "pipemaster-changes: the commit before the push ($BEFORE) is not in the clone, as after a force push; GitLab documents no verdict for this" >&2
        exit 1
      fi
      diff_paths "$BEFORE" "$AFTER"
    fi ;;
  pull_request)
`)
	if fetch {
		sb.WriteString("    fetch_commits \"$BASE\" \"$HEAD\"\n")
	}
	sb.WriteString(`    diff_paths "$BASE...$HEAD" ;;
  *)
    all=true ;;
esac
hit() {
  # Each NUL-separated name is matched as a whole string: bash [[ =~ ]] anchors at the string ends, where grep
  # would also anchor at a newline inside a name.
  if [ "$all" = true ]; then
    echo true
    return
  fi
  while IFS= read -r -d '' name; do
    if [[ $name =~ $1 ]]; then
      echo true
      return
    fi
  done < "$files"
  echo false
}
`)
	for _, a := range ChangesAtomsOf(plans) {
		sb.WriteString("A_" + a.Out + `="$(hit "$RE_` + a.Out + `")"` + "\n")
	}
	for _, p := range plans {
		sb.WriteString("res=false\nif ")
		for i, t := range p.Terms {
			if i > 0 {
				sb.WriteString(" || ")
			}
			sb.WriteString("{ ")
			for j, v := range t {
				if j > 0 {
					sb.WriteString(" && ")
				}
				sb.WriteString(`[ "$A_` + p.Atoms[j].Out + `" = ` + map[bool]string{true: "true", false: "false"}[v] + ` ]`)
			}
			sb.WriteString("; }")
		}
		sb.WriteString("; then res=true; fi\n")
		sb.WriteString(`printf '%s' "$res" > ` + dest(p.Out) + "\n")
	}
	return sb.String()
}
