import Pipemaster.Expr
import Pipemaster.Translate
import Pipemaster.TranslateClass

namespace Pipemaster
namespace Woodpecker

open Pipemaster.Expr Pipemaster.Translate

/-! Woodpecker `evaluate` is expr-lang over a `map[string]string` environment (constraint.go, v3.20.1).
A name the map lacks reads as "" there, not as null; the GitLab class reads an unset variable as null
and never equates null with "". The two rules disagree, so Woodpecker is not in the GitLab class
(whole-class claim refused). The model below covers the part that decides this: == between strings and
null. `GlSub` is the subset the translator uses (matches against a non-empty literal under !, && and ||),
where the two readings agree. -/

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

/-! ### The subset where Woodpecker agrees with the GitLab class

`GlSub` holds for regex tests of a ref against a non-empty literal, combined with `!`, `&&` and `||`. Every
node is boolean-valued, so expr-lang type-checks them, and a ref is read only inside a regex test. -/

/-- Mirrors woodpecker.inSubset (expr/woodpecker/subset.go). -/
def GlSub : E → Bool
  | .rx (.ref _) _ s | .nrx (.ref _) _ s => s != ""
  | .not x => GlSub x
  | .and l r | .or l r => GlSub l && GlSub r
  | _ => false

theorem lower_ne_empty {s : String} (h : s ≠ "") : lower s ≠ "" := by
  intro hl
  apply h
  have : (s.toList.map Char.toLower) = [] := by
    have := congrArg String.toList hl
    simpa [lower] using this
  have h2 : s.toList = [] := List.map_eq_nil_iff.mp this
  exact String.toList_eq_nil_iff.mp h2

/-- A non-empty literal never matches the empty string, in any mode. -/
theorem modeTest_empty (m : Mode) {s : String} (h : s ≠ "") : modeTest m "" (lower s) = false := by
  have hl := lower_ne_empty h
  obtain ⟨a, t, ht⟩ : ∃ a t, (lower s).toList = a :: t := by
    cases hx : (lower s).toList with
    | nil => exact absurd (String.toList_eq_nil_iff.mp hx) hl
    | cons a t => exact ⟨a, t, rfl⟩
  have hne : ("" == lower s) = false := by
    simp only [beq_eq_false_iff_ne, ne_eq]
    exact fun he => hl he.symm
  cases m <;> simp [modeTest, isInfix, ht, hne]

/-- A regex test of a non-empty literal reads an unset ref ("" in Woodpecker) as it reads null. -/
theorem matchV_unset (m : Mode) {s : String} (h : s ≠ "") (vA vB : View) (r : Ref)
    (hr : vB r = some ((vA r).getD "")) : matchV m s (refV vB r) = matchV m s (refV vA r) := by
  cases hA : vA r with
  | none => simp [refV, hr, hA, matchV, lower_empty, modeTest_empty m h]
  | some x => simp [refV, hr, hA]

/-- On `GlSub` expressions, Woodpecker evaluation (an unset ref reads "") equals GitLab-class evaluation. -/
theorem eval_woodpecker (vA vB : View) :
    ∀ e : E, GlSub e = true → (∀ r ∈ TranslateClass.refs e, vB r = some ((vA r).getD "")) →
      eval .gitlab vB e = eval .gitlab vA e := by
  intro e
  induction e with
  | rx x m s =>
    intro h hr
    cases x with
    | ref r =>
      have hs : s ≠ "" := by simpa [GlSub] using h
      have hr' := hr r (by simp [TranslateClass.refs])
      simp [eval, matchV_unset m hs vA vB r hr']
    | _ => simp [GlSub] at h
  | nrx x m s =>
    intro h hr
    cases x with
    | ref r =>
      have hs : s ≠ "" := by simpa [GlSub] using h
      have hr' := hr r (by simp [TranslateClass.refs])
      simp [eval, matchV_unset m hs vA vB r hr']
    | _ => simp [GlSub] at h
  | not x ih =>
    intro h hr
    simp only [GlSub] at h
    simp only [eval]
    rw [ih h (by simpa [TranslateClass.refs] using hr)]
  | and l r ihl ihr =>
    intro h hr
    simp only [GlSub, Bool.and_eq_true] at h
    simp only [TranslateClass.refs, List.mem_append] at hr
    simp only [eval]
    rw [ihl h.1 (fun x hx => hr x (Or.inl hx)), ihr h.2 (fun x hx => hr x (Or.inr hx))]
  | or l r ihl ihr =>
    intro h hr
    simp only [GlSub, Bool.and_eq_true] at h
    simp only [TranslateClass.refs, List.mem_append] at hr
    simp only [eval]
    rw [ihl h.1 (fun x hx => hr x (Or.inl hx)), ihr h.2 (fun x hx => hr x (Or.inr hx))]
  | _ => intro h; simp [GlSub] at h

/-- The truthiness form: a `GlSub` expression holds in Woodpecker exactly when it holds in the GitLab class. -/
theorem woodpecker_sound (vA vB : View) {e : E} (h : GlSub e = true)
    (hr : ∀ r ∈ TranslateClass.refs e, vB r = some ((vA r).getD "")) :
    holds .gitlab vB e = holds .gitlab vA e := by
  unfold holds; rw [eval_woodpecker vA vB e h hr]

/-- An event test `event =~ /^literal$/i` keeps its result when the literal moves from the GitLab-class
event table `tA` (unset is null) to the Woodpecker table `tB` (unset is ""). -/
theorem woodpecker_event_rx {tA tB : Ev → String} (hA : TranslateClass.EvTab tA) (hB : TranslateClass.EvTab tB)
    (env : Env) {vA vB : View} (ha : vA .event = env.event.map tA)
    (hb : vB .event = some ((env.event.map tB).getD ""))
    {e : Ev} {s : String} (hs : lower s = lower (tA e)) :
    holds .gitlab vB (.rx (.ref .event) .exact (tB e)) = holds .gitlab vA (.rx (.ref .event) .exact s) ∧
    holds .gitlab vB (.nrx (.ref .event) .exact (tB e)) = holds .gitlab vA (.nrx (.ref .event) .exact s) := by
  have key : matchV .exact (tB e) (refV vB .event) = matchV .exact s (refV vA .event) := by
    cases hev : env.event with
    | none =>
      have ha' : vA .event = none := by rw [ha, hev]; rfl
      have hb' : vB .event = some "" := by rw [hb, hev]; rfl
      have hne : lower (tB e) ≠ "" := hB.ne e
      simp [refV, ha', hb', matchV, modeTest, lower_empty, Ne.symm hne]
    | some e0 =>
      have ha' : vA .event = some (tA e0) := by rw [ha, hev]; rfl
      have hb' : vB .event = some (tB e0) := by rw [hb, hev]; rfl
      have h1 := TranslateClass.fold_beq hA .gha e0 e
      have h2 := TranslateClass.fold_beq hB .gha e0 e
      simp only [TranslateClass.foldS] at h1 h2
      rw [← hs] at h1
      simp [refV, ha', hb', matchV, modeTest, h1, h2]
  constructor <;> simp [holds, eval, truthy, key]

end Woodpecker
end Pipemaster
