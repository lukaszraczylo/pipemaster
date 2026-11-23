import Pipemaster.Translate

namespace Pipemaster
namespace TranslateFacts

open Pipemaster.Expr Pipemaster.Translate

/-! Trigger facts for GHA `ref_name` conditions (expr.TranslateFacts, expr/translate_facts.go).

GHA `ref_name` is a branch or a tag name, so no GitLab-class dialect that names only the branch ref can
state `ref_name == 'main'` alone. When the pipeline's triggers rule out tag runs, every run that is not a
pull request has a branch ref. On a pull request `ref_name` is `<number>/merge`, which never equals a
literal that does not start with a digit or a minus sign. The rewrite below states both facts in the
condition, and the translator then folds the branch guard into the branch ref. -/


theorem isDigit_iff (c : Char) : c.isDigit = true ↔ 48 ≤ c.val.toNat ∧ c.val.toNat ≤ 57 := by
  simp only [Char.isDigit, Bool.and_eq_true, decide_eq_true_eq, UInt32.le_iff_toNat_le]
  simp

theorem toLower_digit (c : Char) : (Char.toLower c).isDigit = c.isDigit := by
  unfold Char.toLower
  split
  · rename_i h
    obtain ⟨h1, h2⟩ := h
    have a : 65 ≤ c.val.toNat := by
      have := UInt32.le_iff_toNat_le.mp h1; simpa using this
    have b : c.val.toNat ≤ 90 := by
      have := UInt32.le_iff_toNat_le.mp h2; simpa using this
    have hd : c.isDigit = false := by
      cases h : c.isDigit
      · rfl
      · have := (isDigit_iff c).mp h; omega
    rw [hd]
    cases h : (Char.mk (c.val + ('a'.val - 'A'.val)) _).isDigit
    · rfl
    · have := (isDigit_iff _).mp h
      simp [UInt32.toNat_add] at this
      have e1 : c.toNat = c.val.toNat := rfl
      have e2 : c.val.toNat < 4294967296 := UInt32.toNat_lt _
      omega
  · rfl

theorem eqHyphen_iff (c : Char) : (c == '-') = true ↔ c.val.toNat = 45 := by
  simp only [beq_iff_eq]
  constructor
  · intro h; subst h; rfl
  · intro h
    apply Char.ext
    apply UInt32.toNat_inj.mp
    simpa using h

theorem toLower_hyphen (c : Char) : (Char.toLower c == '-') = (c == '-') := by
  unfold Char.toLower
  split
  · rename_i h
    obtain ⟨h1, h2⟩ := h
    have a : 65 ≤ c.val.toNat := by
      have := UInt32.le_iff_toNat_le.mp h1; simpa using this
    have b : c.val.toNat ≤ 90 := by
      have := UInt32.le_iff_toNat_le.mp h2; simpa using this
    have hd : (c == '-') = false := by
      cases h : (c == '-')
      · rfl
      · have := (eqHyphen_iff c).mp h; omega
    rw [hd]
    cases h : (Char.mk (c.val + ('a'.val - 'A'.val)) _ == '-')
    · rfl
    · have := (eqHyphen_iff _).mp h
      simp [UInt32.toNat_add] at this
      have e1 : c.toNat = c.val.toNat := rfl
      have e2 : c.val.toNat < 4294967296 := UInt32.toNat_lt _
      omega
  · rfl

theorem numLike_lower (s : String) : numLike (lower s) = numLike s := by
  unfold lower numLike
  simp only [String.toList_ofList]
  cases s.toList with
  | nil => rfl
  | cons c cs => simp [toLower_digit, toLower_hyphen]

/-- A string that GHA reads as a number never equals, after lowering, one that it does not. -/
theorem lower_ne_of_numLike {r s : String} (hr : numLike r = true) (hs : okEq s = true) :
    (lower r == lower s) = false := by
  have hs' : numLike s = false := by
    simp [okEq] at hs
    simpa using hs.2
  by_contra h
  have h' : lower r = lower s := by simpa using h
  have := congrArg numLike h'
  rw [numLike_lower, numLike_lower, hr, hs'] at this
  exact absurd this (by decide)

/-- What the trigger set guarantees about a run (Mirrors expr.Facts (expr/translate_facts.go)): a run that is
not a pull request has a branch ref, and a pull request run names its ref `<number>/merge`. -/
structure BranchFact (env : Env) : Prop where
  branch : env.event ≠ some .pullRequest → env.refType = some .branch
  pr : env.event = some .pullRequest → ∀ r, env.refName = some r → numLike r = true

def evIsPr : E := .eq (.ref .event) (.str "pull_request")
def evNotPr : E := .ne (.ref .event) (.str "pull_request")
def brGuard : E := .eq (.ref .refType) (.str "branch")

/-- A comparison of ref_name with a literal the translator can state, in either operand order. -/
def isNameCmp : E → E → Bool
  | .ref .refName, .str s | .str s, .ref .refName => okEq s
  | _, _ => false

/-- A `==` of ref_name with a literal the translator can state. -/
def isEqCmp : E → Bool
  | .eq l r => isNameCmp l r
  | _ => false

/-- Mirrors expr.AssumeBranch (expr/translate_facts.go): states the trigger facts in each ref_name comparison.
A branch guard on such a comparison names the branch already, so it stays and only the pull request test is
added. Every other guarded test stays as it is. -/
def assumeBranch : E → E
  | .eq l r => if isNameCmp l r then .and evNotPr (.and brGuard (.eq l r)) else .eq l r
  | .ne l r => if isNameCmp l r then .or evIsPr (.not (.and brGuard (.eq l r))) else .ne l r
  | .not x => .not (assumeBranch x)
  | .and l r =>
    if typeGuard l = some .branch && isEqCmp r then .and evNotPr (.and l r)
    else if (typeGuard l).isSome && nameTest r then .and l r
    else .and (assumeBranch l) (assumeBranch r)
  | .or l r => .or (assumeBranch l) (assumeBranch r)
  | e => e

theorem eval_evIsPr (env : Env) :
    eval .gha (ghaView env) evIsPr = .bool (decide (env.event = some .pullRequest)) := by
  cases h : env.event with
  | none => simp [evIsPr, eval, refV, ghaView, h, eqV]
  | some e => cases e <;> simp [evIsPr, eval, refV, ghaView, h, eqV, evGha, lower]

theorem eval_evNotPr (env : Env) :
    eval .gha (ghaView env) evNotPr = .bool (!decide (env.event = some .pullRequest)) := by
  cases h : env.event with
  | none => simp [evNotPr, eval, refV, ghaView, h, eqV]
  | some e => cases e <;> simp [evNotPr, eval, refV, ghaView, h, eqV, evGha, lower]

theorem eval_brGuard (env : Env) (h : env.refType = some .branch) :
    eval .gha (ghaView env) brGuard = .bool true := by
  simp [brGuard, eval, refV, ghaView, h, eqV, rtName]

/-- On a pull request the ref_name comparison is false. -/
theorem nameCmp_pr {env : Env} (F : BranchFact env) (hp : env.event = some .pullRequest) {l r : E}
    (h : isNameCmp l r = true) : eval .gha (ghaView env) (.eq l r) = .bool false := by
  have key : ∀ s, okEq s = true →
      eqV .gha (refV (ghaView env) .refName) (.str s) = false := by
    intro s hs
    have hne : (s == "") = false := by
      simp [okEq, okLit] at hs
      simpa using hs.1.1
    cases hn : env.refName with
    | none => simp [refV, ghaView, hn, eqV, hne]
    | some r0 =>
      have := lower_ne_of_numLike (F.pr hp r0 hn) hs
      simp [refV, ghaView, hn, eqV, this]
  unfold isNameCmp at h
  split at h
  · rename_i s
    simp [eval, key s h]
  · rename_i s
    simp only [eval]
    rw [eqV_comm]
    simp [key s h]
  · simp at h

theorem eval_assume_cmp {env : Env} (F : BranchFact env) {l r : E} (h : isNameCmp l r = true) :
    eval .gha (ghaView env) (.and evNotPr (.and brGuard (.eq l r))) = eval .gha (ghaView env) (.eq l r) := by
  by_cases hp : env.event = some .pullRequest
  · rw [nameCmp_pr F hp h]
    simp [eval, eval_evNotPr, hp, logicalTruthy, truthy]
  · have hg := eval_brGuard env (F.branch hp)
    simp [eval, eval_evNotPr, hp, hg, logicalTruthy, truthy]

theorem eval_assume_ne {env : Env} (F : BranchFact env) {l r : E} (h : isNameCmp l r = true) :
    eval .gha (ghaView env) (.or evIsPr (.not (.and brGuard (.eq l r)))) = eval .gha (ghaView env) (.ne l r) := by
  have hne : eval .gha (ghaView env) (.ne l r) =
      .bool (!truthy (eval .gha (ghaView env) (.eq l r))) := by
    simp only [eval]
    cases hc : eval .gha (ghaView env) l <;> cases hd : eval .gha (ghaView env) r <;> simp [eqV, truthy]
  by_cases hp : env.event = some .pullRequest
  · have := nameCmp_pr F hp h
    rw [hne, this]
    simp [eval, eval_evIsPr, hp, logicalTruthy, truthy]
  · have hg := eval_brGuard env (F.branch hp)
    rw [hne]
    simp [eval, eval_evIsPr, hp, hg, logicalTruthy, truthy]

theorem eval_assume_guard {env : Env} (F : BranchFact env) {l r : E} (hg : typeGuard l = some .branch)
    (hr : isEqCmp r = true) :
    eval .gha (ghaView env) (.and evNotPr (.and l r)) = eval .gha (ghaView env) (.and l r) := by
  obtain ⟨a, b, rfl⟩ : ∃ a b, r = .eq a b := by
    cases r <;> simp [isEqCmp] at hr
    exact ⟨_, _, rfl⟩
  have hc : isNameCmp a b = true := by simpa [isEqCmp] using hr
  have hl := typeGuard_eval env hg
  by_cases hp : env.event = some .pullRequest
  · have h1 : eqV .gha (eval .gha (ghaView env) a) (eval .gha (ghaView env) b) = false := by
      simpa [eval] using nameCmp_pr F hp hc
    by_cases ht : env.refType = some .branch
    · simp [eval, eval_evNotPr, hp, hl, ht, h1, logicalTruthy, truthy]
    · simp [eval, eval_evNotPr, hp, hl, ht, logicalTruthy, truthy]
  · have ht := F.branch hp
    simp [eval, eval_evNotPr, hp, hl, ht, logicalTruthy, truthy]

/-- The rewrite does not change the value of any expression, on an environment the facts allow. -/
theorem eval_assume {env : Env} (F : BranchFact env) :
    ∀ e : E, eval .gha (ghaView env) (assumeBranch e) = eval .gha (ghaView env) e := by
  intro e
  induction e with
  | eq l r =>
    simp only [assumeBranch]
    split
    · exact eval_assume_cmp F ‹_›
    · rfl
  | ne l r =>
    simp only [assumeBranch]
    split
    · exact eval_assume_ne F ‹_›
    · rfl
  | not x ih => simp [assumeBranch, eval, ih]
  | and l r ihl ihr =>
    simp only [assumeBranch]
    split
    · rename_i hg
      simp only [Bool.and_eq_true, decide_eq_true_eq] at hg
      exact eval_assume_guard F hg.1 hg.2
    · split
      · rfl
      · simp [eval, ihl, ihr]
  | or l r ihl ihr => simp [assumeBranch, eval, ihl, ihr]
  | _ => rfl

/-- The expressions that translate once the facts are stated (Mirrors expr.TranslatableFacts (expr/translate_facts.go)). -/
def TranslatableFacts (e : E) : Prop := Translatable (assumeBranch e)

/-- Under the trigger facts, the translation of the rewritten condition has the truthiness of the source on
every environment the facts allow. -/
theorem trFacts_sound {e : E} (env : Env) (F : BranchFact env) (h : TranslatableFacts e) :
    ∃ e', tr (assumeBranch e) = some e' ∧
      holds .gitlab (mapEnv env) e' = holds .gha (ghaView env) e := by
  obtain ⟨e', he', hh⟩ := tr_sound env h
  refine ⟨e', he', ?_⟩
  rw [hh]
  unfold holds
  rw [eval_assume F e]

end TranslateFacts
end Pipemaster
