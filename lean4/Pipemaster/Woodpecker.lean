import Pipemaster.Expr

namespace Pipemaster
namespace Woodpecker

open Pipemaster.Expr

/-! Woodpecker `evaluate` is expr-lang over a `map[string]string` environment (constraint.go, v3.20.1).
A name the map lacks reads as "" there, not as null; the GitLab class reads an unset variable as null
and never equates null with "". The two rules disagree, so Woodpecker is not in the GitLab class
(expr.ClassWoodpecker) and translates nothing. The model below covers the part that decides this:
== between strings and null. -/

/-- Mirrors woodpecker.UnsetEnv (expr/woodpecker/vocab.go): a ref the pipeline does not set reads as "". -/
def wpView (v : View) : View := fun r => some ((v r).getD "")

/-- Where the classes agree: a ref the pipeline sets. -/
theorem wpView_set {v : View} {r : Ref} {s : String} (h : v r = some s) : wpView v r = v r := by
  simp [wpView, h]

/-- The environment of a branch run: canonical tag is unset, as CI_COMMIT_TAG is for GitLab. -/
def branchEnv : Env := ⟨some "main", some .branch, some .push, some "abc", none⟩

/-- `tag == ""` holds in Woodpecker and fails in the GitLab class on the same run. -/
theorem eq_empty_differs :
    eval .gitlab (wpView (mapEnv branchEnv)) (.eq (.ref .tag) (.str "")) = .bool true ∧
    eval .gitlab (mapEnv branchEnv) (.eq (.ref .tag) (.str "")) = .bool false := by
  decide +kernel

/-- `tag == null` fails in Woodpecker and holds in the GitLab class on the same run. -/
theorem eq_null_differs :
    eval .gitlab (wpView (mapEnv branchEnv)) (.eq (.ref .tag) .null) = .bool false ∧
    eval .gitlab (mapEnv branchEnv) (.eq (.ref .tag) .null) = .bool true := by
  decide +kernel

/-- No translation from the GitLab class to Woodpecker, or back, can be exact for every expression:
some expression evaluates differently under the two readings of the same run. -/
theorem not_gitlab_class :
    ¬ ∀ e : E, eval .gitlab (wpView (mapEnv branchEnv)) e = eval .gitlab (mapEnv branchEnv) e := by
  intro h
  have := h (.eq (.ref .tag) (.str ""))
  have h2 := eq_empty_differs
  rw [this] at h2
  have := h2.1.symm.trans h2.2
  cases this

end Woodpecker
end Pipemaster
