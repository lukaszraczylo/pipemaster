import Pipemaster.Translate
import Pipemaster.TranslateClass
import Pipemaster.Woodpecker

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

/-- What every pipeline with a pull request trigger guarantees (Mirrors expr.Facts (expr/translate_facts.go)):
a pull request run has a branch ref type and names its ref `<number>/merge`. -/
structure PrFact (env : Env) : Prop where
  ref : env.event = some .pullRequest → env.refType = some .branch
  name : env.event = some .pullRequest → ∀ r, env.refName = some r → numLike r = true

/-- The facts of a trigger set with no tag trigger: also, a run that is not a pull request has a branch ref. -/
structure BranchFact (env : Env) : Prop extends PrFact env where
  branch : env.event ≠ some .pullRequest → env.refType = some .branch

/-- The facts `assume fa` relies on: `fa` says the branch facts hold. -/
def Fact (fa : Bool) (env : Env) : Prop :=
  PrFact env ∧ (fa = true → env.event ≠ some .pullRequest → env.refType = some .branch)

theorem BranchFact.fact {env : Env} (F : BranchFact env) : Fact true env :=
  ⟨F.toPrFact, fun _ => F.branch⟩

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

/-- Mirrors expr.Assume (expr/translate_facts.go): states the pull request facts in each branch-guarded ref_name
equality and, when `fa` says the branch facts hold, in each ref_name comparison. A branch guard on such an equality
names the branch already, so it stays and only the pull request test is added. Every other guarded test stays. -/
def assume (fa : Bool) : E → E
  | .eq l r => if fa && isNameCmp l r then .and evNotPr (.and brGuard (.eq l r)) else .eq l r
  | .ne l r => if fa && isNameCmp l r then .or evIsPr (.not (.and brGuard (.eq l r))) else .ne l r
  | .not x => .not (assume fa x)
  | .and l r =>
    if typeGuard l = some .branch && isEqCmp r then .and evNotPr (.and l r)
    else if (typeGuard l).isSome && nameTest r then .and l r
    else .and (assume fa l) (assume fa r)
  | .or l r => .or (assume fa l) (assume fa r)
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
theorem nameCmp_pr {env : Env} (F : PrFact env) (hp : env.event = some .pullRequest) {l r : E}
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
      have := lower_ne_of_numLike (F.name hp r0 hn) hs
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

theorem eval_assume_cmp {env : Env} (F : PrFact env) (hb : env.event ≠ some .pullRequest → env.refType = some .branch)
    {l r : E} (h : isNameCmp l r = true) :
    eval .gha (ghaView env) (.and evNotPr (.and brGuard (.eq l r))) = eval .gha (ghaView env) (.eq l r) := by
  by_cases hp : env.event = some .pullRequest
  · rw [nameCmp_pr F hp h]
    simp [eval, eval_evNotPr, hp, logicalTruthy, truthy]
  · have hg := eval_brGuard env (hb hp)
    simp [eval, eval_evNotPr, hp, hg, logicalTruthy, truthy]

theorem eval_assume_ne {env : Env} (F : PrFact env) (hb : env.event ≠ some .pullRequest → env.refType = some .branch)
    {l r : E} (h : isNameCmp l r = true) :
    eval .gha (ghaView env) (.or evIsPr (.not (.and brGuard (.eq l r)))) = eval .gha (ghaView env) (.ne l r) := by
  have hne : eval .gha (ghaView env) (.ne l r) =
      .bool (!truthy (eval .gha (ghaView env) (.eq l r))) := by
    simp only [eval]
    cases hc : eval .gha (ghaView env) l <;> cases hd : eval .gha (ghaView env) r <;> simp [eqV, truthy]
  by_cases hp : env.event = some .pullRequest
  · have := nameCmp_pr F hp h
    rw [hne, this]
    simp [eval, eval_evIsPr, hp, logicalTruthy, truthy]
  · have hg := eval_brGuard env (hb hp)
    rw [hne]
    simp [eval, eval_evIsPr, hp, hg, logicalTruthy, truthy]

theorem eval_assume_guard {env : Env} (F : PrFact env) {l r : E} (hg : typeGuard l = some .branch)
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
  · simp [eval, eval_evNotPr, hp, logicalTruthy, truthy]

/-- The rewrite does not change the value of any expression, on an environment the facts allow. -/
theorem eval_assume {fa : Bool} {env : Env} (F : Fact fa env) :
    ∀ e : E, eval .gha (ghaView env) (assume fa e) = eval .gha (ghaView env) e := by
  intro e
  induction e with
  | eq l r =>
    simp only [assume]
    split
    · rename_i h
      simp only [Bool.and_eq_true] at h
      obtain ⟨rfl, hc⟩ := h
      exact eval_assume_cmp F.1 (F.2 rfl) hc
    · rfl
  | ne l r =>
    simp only [assume]
    split
    · rename_i h
      simp only [Bool.and_eq_true] at h
      obtain ⟨rfl, hc⟩ := h
      exact eval_assume_ne F.1 (F.2 rfl) hc
    · rfl
  | not x ih => simp [assume, eval, ih]
  | and l r ihl ihr =>
    simp only [assume]
    split
    · rename_i hg
      simp only [Bool.and_eq_true, decide_eq_true_eq] at hg
      exact eval_assume_guard F.1 hg.1 hg.2
    · split
      · rfl
      · simp [eval, ihl, ihr]
  | or l r ihl ihr => simp [assume, eval, ihl, ihr]
  | _ => rfl

/-- The expressions that translate once the facts are stated (Mirrors expr.TranslatableFacts (expr/translate_facts.go)). -/
def TranslatableFacts (fa : Bool) (e : E) : Prop := Translatable (assume fa e)

/-- Under the facts, the translation of the rewritten condition has the truthiness of the source on
every environment the facts allow. -/
theorem trFacts_sound {fa : Bool} {e : E} (env : Env) (F : Fact fa env) (h : TranslatableFacts fa e) :
    ∃ e', tr (assume fa e) = some e' ∧
      holds .gitlab (mapEnv env) e' = holds .gha (ghaView env) e := by
  obtain ⟨e', he', hh⟩ := tr_sound env h
  refine ⟨e', he', ?_⟩
  rw [hh]
  unfold holds
  rw [eval_assume F e]


/-! ### Woodpecker

Woodpecker is in the GitLab class, but on a pull request CI_COMMIT_BRANCH holds the target branch and
CI_COMMIT_TAG is unset. `wpMap` is that environment; `mapEnv` gives the source ref there. A translation that
tests the branch ref without excluding pull requests does not agree with the source on `wpMap`. -/

/-- Mirrors the Woodpecker variables (expr/woodpecker/vocab.go): a pull request run has the target branch `tgt`
in CI_COMMIT_BRANCH and no CI_COMMIT_TAG. -/
def wpMap (env : Env) (tgt : Option String) : View
  | .branch => if env.event = some .pullRequest then tgt else mapEnv env .branch
  | .tag => if env.event = some .pullRequest then none else mapEnv env .tag
  | r => mapEnv env r

/-- The event test `event !~ /^merge_request_event$/i`: false on a pull request. -/
def guardsPr : E → Bool
  | .nrx (.ref .event) .exact s => s == "merge_request_event"
  | _ => false

/-- The event test `event =~ /^merge_request_event$/i`: true on a pull request. -/
def guardsPrOr : E → Bool
  | .rx (.ref .event) .exact s => s == "merge_request_event"
  | _ => false

/-- Every read of the branch ref sits behind a pull request test, so the value of the expression does not
depend on what CI_COMMIT_BRANCH holds on a pull request. -/
def PRG : E → Bool
  | .ref r => r != .branch
  | .not x => PRG x
  | .and l r => guardsPr l || (PRG l && PRG r)
  | .or l r => guardsPrOr l || (PRG l && PRG r)
  | .eq l r | .ne l r | .lt l r | .startsWith l r | .endsWith l r | .contains l r => PRG l && PRG r
  | .rx x _ _ | .nrx x _ _ | .rawRx x _ | .call _ x => PRG x
  | _ => true

theorem wpMap_eq_mapEnv {env : Env} (tgt : Option String) (hp : env.event ≠ some .pullRequest) :
    wpMap env tgt = mapEnv env := by
  funext r
  cases r <;> simp [wpMap, hp]

theorem guardsPr_eval {env : Env} (V : View) (hV : V .event = mapEnv env .event) (hp : env.event = some .pullRequest)
    {l : E} (h : guardsPr l = true) : eval .gitlab V l = .bool false := by
  unfold guardsPr at h
  split at h
  · rename_i s
    have hs : s = "merge_request_event" := by simpa using h
    subst hs
    simp [eval, refV, hV, mapEnv, hp, evGl, matchV, modeTest]
  · simp at h

theorem guardsPrOr_eval {env : Env} (V : View) (hV : V .event = mapEnv env .event) (hp : env.event = some .pullRequest)
    {l : E} (h : guardsPrOr l = true) : eval .gitlab V l = .bool true := by
  unfold guardsPrOr at h
  split at h
  · rename_i s
    have hs : s = "merge_request_event" := by simpa using h
    subst hs
    simp [eval, refV, hV, mapEnv, hp, evGl, matchV, modeTest]
  · simp at h

/-- Where the branch ref is behind a pull request test, Woodpecker and the GitLab class agree. -/
theorem view_change (env : Env) (tgt : Option String)
    (hr : env.event = some .pullRequest → env.refType = some .branch) :
    ∀ e : E, PRG e = true → eval .gitlab (wpMap env tgt) e = eval .gitlab (mapEnv env) e := by
  by_cases hp : env.event = some .pullRequest
  · intro e
    have hev : wpMap env tgt .event = mapEnv env .event := by simp [wpMap]
    induction e with
    | ref r =>
      intro h
      have hr' : r ≠ .branch := by simpa [PRG] using h
      have ht := hr hp
      cases r <;> simp [eval, refV, wpMap, mapEnv, hp, ht] at hr' ⊢
    | and l r ihl ihr =>
      intro h
      simp only [PRG, Bool.or_eq_true, Bool.and_eq_true] at h
      rcases h with hg | ⟨h1, h2⟩
      · have a := guardsPr_eval (wpMap env tgt) hev hp hg
        have b := guardsPr_eval (mapEnv env) rfl hp hg
        simp [eval, a, b, logicalTruthy, rubyTruthy]
      · simp [eval, ihl h1, ihr h2]
    | or l r ihl ihr =>
      intro h
      simp only [PRG, Bool.or_eq_true, Bool.and_eq_true] at h
      rcases h with hg | ⟨h1, h2⟩
      · have a := guardsPrOr_eval (wpMap env tgt) hev hp hg
        have b := guardsPrOr_eval (mapEnv env) rfl hp hg
        simp [eval, a, b, logicalTruthy, rubyTruthy]
      · simp [eval, ihl h1, ihr h2]
    | not x ih => intro h; simp [eval, ih (by simpa [PRG] using h)]
    | eq l r ihl ihr =>
      intro h
      simp only [PRG, Bool.and_eq_true] at h
      simp [eval, ihl h.1, ihr h.2]
    | ne l r ihl ihr =>
      intro h
      simp only [PRG, Bool.and_eq_true] at h
      simp [eval, ihl h.1, ihr h.2]
    | lt l r ihl ihr => intro _; simp [eval]
    | startsWith l r ihl ihr => intro _; simp [eval]
    | endsWith l r ihl ihr => intro _; simp [eval]
    | contains l r ihl ihr => intro _; simp [eval]
    | call n x ih => intro _; simp [eval]
    | rx x m s ih => intro h; simp [eval, ih (by simpa [PRG] using h)]
    | nrx x m s ih => intro h; simp [eval, ih (by simpa [PRG] using h)]
    | _ => intro _; simp [eval]
  · intro e _
    rw [wpMap_eq_mapEnv tgt hp]


/-- Translated comparisons read only the refs ref_name, sha and event. -/
theorem trEq_PRG {neg : Bool} {l r e' : E} (h : trEq neg l r = some e') : PRG e' = true := by
  have key : ∀ (x : Ref) (s : String), trCmp neg x s = some e' → PRG e' = true := by
    intro x s hx
    cases x
    case event =>
      simp only [trCmp] at hx
      obtain ⟨e0, _, rfl⟩ := Option.map_eq_some_iff.mp hx
      cases neg <;> simp [rxNode, PRG]
    case refName | sha =>
      simp only [trCmp] at hx
      split at hx
      · cases hx; cases neg <;> simp [rxNode, PRG]
      · simp at hx
    all_goals simp [trCmp] at hx
  unfold trEq at h
  split at h
  · exact key _ _ h
  · exact key _ _ h
  · simp at h

theorem trAffE_PRG {m : Mode} {a b e' : E} (h : trAffE m a b = some e') : PRG e' = true := by
  unfold trAffE at h
  split at h
  · rename_i x s
    cases x
    case refName | sha =>
      simp only [trAff] at h
      split at h
      · cases h; simp [PRG]
      · simp at h
    all_goals simp [trAff] at h
  · simp at h

theorem isEqCmp_nameTest {r : E} (h : isEqCmp r = true) : nameTest r = true := by
  cases r <;> simp [isEqCmp] at h
  rename_i a b
  unfold isNameCmp at h
  split at h <;> simp_all [nameTest]

theorem tr_guard_eq {l r : E} (hg : typeGuard l = some .branch) (hr : isEqCmp r = true) :
    ∃ m s, tr (.and l r) = some (.rx (.ref .branch) m s) := by
  obtain ⟨m, s, h⟩ := nameTest_tr (isEqCmp_nameTest hr)
  exact ⟨m, s, by simp [tr, hg, h, rtRef]⟩

theorem tr_evNotPr : tr evNotPr = some (.nrx (.ref .event) .exact "merge_request_event") := by
  have : lower "pull_request" = "pull_request" := by decide
  simp [evNotPr, tr, trEq, trCmp, evOfGha, this, rxNode, evGl]

theorem tr_evIsPr : tr evIsPr = some (.rx (.ref .event) .exact "merge_request_event") := by
  have : lower "pull_request" = "pull_request" := by decide
  simp [evIsPr, tr, trEq, trCmp, evOfGha, this, rxNode, evGl]

theorem isNameCmp_typeGuard {a b : E} (h : isNameCmp a b = true) : typeGuard (.eq a b) = none := by
  unfold isNameCmp at h
  split at h
  · simp [typeGuard]
  · simp [typeGuard]
  · simp at h

theorem typeGuard_assume (fa : Bool) (l : E) : typeGuard (assume fa l) = typeGuard l := by
  cases l
  case eq a b =>
    simp only [assume]
    split
    · rename_i h
      simp only [Bool.and_eq_true] at h
      rw [isNameCmp_typeGuard h.2]
      rfl
    · rfl
  case ne a b =>
    simp only [assume]
    split <;> simp [typeGuard]
  case not x => simp [assume, typeGuard]
  case and a b =>
    simp only [assume]
    split
    · simp [typeGuard]
    · split <;> simp [typeGuard]
  case or a b => simp [assume, typeGuard]
  all_goals rfl

theorem nameTest_assume {fa : Bool} {r : E} (h : nameTest (assume fa r) = true) : nameTest r = true := by
  cases r
  case eq a b =>
    simp only [assume] at h
    split at h
    · simp [nameTest] at h
    · exact h
  case ne a b =>
    simp only [assume] at h
    split at h <;> simp [nameTest] at h
  case not x => simp [assume, nameTest] at h
  case and a b =>
    simp only [assume] at h
    split at h
    · simp [nameTest] at h
    · split at h <;> simp [nameTest] at h
  case or a b => simp [assume, nameTest] at h
  all_goals exact h

/-- Guarded branch tests that the rewrite cannot make exact on a pull request: a branch guard on an affix test. -/
def guardOk : E → Bool
  | .not x => guardOk x
  | .and l r => !(typeGuard l = some .branch && nameTest r && !isEqCmp r) && guardOk l && guardOk r
  | .or l r => guardOk l && guardOk r
  | _ => true

/-- Mirrors expr.TranslatableFacts (expr/translate_facts.go) for a Woodpecker target. -/
def TranslatableWp (fa : Bool) (e : E) : Prop := guardOk e = true ∧ Translatable (assume fa e)


theorem typeGuard_evNotPr : typeGuard evNotPr = none := by simp [evNotPr, typeGuard]
theorem typeGuard_evIsPr : typeGuard evIsPr = none := by simp [evIsPr, typeGuard]
theorem bv_evNotPr : bv evNotPr = true := by simp [evNotPr, bv]
theorem bv_evIsPr : bv evIsPr = true := by simp [evIsPr, bv]

theorem typeGuard_brGuard : typeGuard brGuard = some .branch := by
  have : lower "branch" = "branch" := by decide
  simp [brGuard, typeGuard, this]

theorem tr_and_ev {X : E} {m : Mode} {s : String} (hX : tr X = some (.rx (.ref .branch) m s)) :
    tr (.and evNotPr X) = some (.and (.nrx (.ref .event) .exact "merge_request_event") (.rx (.ref .branch) m s)) := by
  simp [tr, typeGuard_evNotPr, hX, bv_evNotPr, tr_evNotPr]

theorem tr_or_ev {X : E} {m : Mode} {s : String} (hX : tr X = some (.rx (.ref .branch) m s)) :
    tr (.or evIsPr (.not X)) = some (.or (.rx (.ref .event) .exact "merge_request_event") (.not (.rx (.ref .branch) m s))) := by
  simp [tr, bv_evIsPr, hX, tr_evIsPr]

theorem tr_PRG (fa : Bool) : ∀ e e' : E, guardOk e = true → tr (assume fa e) = some e' → PRG e' = true := by
  intro e
  induction e with
  | eq l r =>
    intro e' _ h
    simp only [assume] at h
    split at h
    · rename_i hc
      simp only [Bool.and_eq_true] at hc
      obtain ⟨m, s, h1⟩ := tr_guard_eq (l := brGuard) (r := .eq l r) typeGuard_brGuard (by simpa [isEqCmp] using hc.2)
      rw [tr_and_ev h1] at h
      cases h
      simp [PRG, guardsPr]
    · exact trEq_PRG (by simpa [tr] using h)
  | ne l r =>
    intro e' _ h
    simp only [assume] at h
    split at h
    · rename_i hc
      simp only [Bool.and_eq_true] at hc
      obtain ⟨m, s, h1⟩ := tr_guard_eq (l := brGuard) (r := .eq l r) typeGuard_brGuard (by simpa [isEqCmp] using hc.2)
      have h2 : tr (.and brGuard (.eq l r)) = some (.rx (.ref .branch) m s) := h1
      rw [tr_or_ev h2] at h
      cases h
      simp [PRG, guardsPrOr]
    · exact trEq_PRG (by simpa [tr] using h)
  | startsWith a b => intro e' _ h; exact trAffE_PRG (by simpa [assume, tr] using h)
  | endsWith a b => intro e' _ h; exact trAffE_PRG (by simpa [assume, tr] using h)
  | contains a b => intro e' _ h; exact trAffE_PRG (by simpa [assume, tr] using h)
  | not x ih =>
    intro e' hg h
    simp only [assume, tr] at h
    obtain ⟨a, ha, rfl⟩ := Option.map_eq_some_iff.mp h
    simpa [PRG] using ih a (by simpa [guardOk] using hg) ha
  | or l r ihl ihr =>
    intro e' hg h
    simp only [assume, tr] at h
    split at h
    · obtain ⟨a, ha, h2⟩ := Option.bind_eq_some_iff.mp h
      obtain ⟨b, hb, rfl⟩ := Option.map_eq_some_iff.mp h2
      simp only [guardOk, Bool.and_eq_true] at hg
      have := ihl a hg.1 ha
      have := ihr b hg.2 hb
      simp [PRG, *]
    · simp at h
  | and l r ihl ihr =>
    intro e' hg h
    simp only [guardOk, Bool.and_eq_true, Bool.not_eq_true'] at hg
    simp only [assume] at h
    split at h
    · rename_i hc
      simp only [Bool.and_eq_true, decide_eq_true_eq] at hc
      obtain ⟨m, s, h1⟩ := tr_guard_eq hc.1 hc.2
      rw [tr_and_ev h1] at h
      cases h
      simp [PRG, guardsPr]
    · rename_i hc
      split at h
      · rename_i hc2
        simp only [Bool.and_eq_true, Option.isSome_iff_exists] at hc2
        obtain ⟨⟨t, ht⟩, hn⟩ := hc2
        obtain ⟨m, s, h1⟩ := nameTest_tr hn
        simp only [tr, ht, h1] at h
        cases h
        cases t
        · exfalso
          have hb : isEqCmp r = false := by
            by_contra hh
            simp only [Bool.and_eq_true, decide_eq_true_eq] at hc
            exact hc ⟨ht, by simpa using hh⟩
          have := hg.1.1
          simp [ht, hn, hb] at this
        · simp [PRG, rtRef]
      · rename_i hc2
        simp only [tr] at h
        split at h
        · rename_i t m s hg1 hr1
          exfalso
          rw [typeGuard_assume] at hg1
          have hn := nameTest_assume (tr_rx_name hr1)
          apply hc2
          simp [hg1, hn]
        · split at h
          · obtain ⟨a, ha, h2⟩ := Option.bind_eq_some_iff.mp h
            obtain ⟨b, hb, rfl⟩ := Option.map_eq_some_iff.mp h2
            have := ihl a hg.1.2 ha
            have := ihr b hg.2 hb
            simp [PRG, *]
          · simp at h
  | ref r =>
    intro e' _ h
    cases r <;> simp [assume, tr] at h <;> (subst h; simp [PRG])
  | _ => intro e' _ h; simp [assume, tr] at h


/-- Under the facts, the Woodpecker translation of the rewritten condition has the truthiness of the source, on
Woodpecker's own environment: a pull request holds the target branch in CI_COMMIT_BRANCH. -/
theorem trFactsWp_sound {fa : Bool} {e : E} (env : Env) (tgt : Option String) (F : Fact fa env)
    (h : TranslatableWp fa e) :
    ∃ e', tr (assume fa e) = some e' ∧
      holds .gitlab (wpMap env tgt) e' = holds .gha (ghaView env) e := by
  obtain ⟨e', he', hh⟩ := trFacts_sound (e := e) env F h.2
  refine ⟨e', he', ?_⟩
  have hp := tr_PRG fa e e' h.1 he'
  unfold holds at hh ⊢
  rw [view_change env tgt F.1.ref e' hp, hh]

/-- Why the pull request test exists: the plain translation of a branch-guarded ref_name test reads the target
branch on a pull request, where the GHA ref_name is `<number>/merge`. -/
theorem guard_pr_unsound : ∃ (env : Env) (tgt : Option String), PrFact env ∧
    holds .gitlab (wpMap env tgt) (.rx (.ref .branch) .exact "main") ≠
      holds .gha (ghaView env) (.and brGuard (.eq (.ref .refName) (.str "main"))) := by
  refine ⟨⟨some "12/merge", some .branch, some .pullRequest, none, none⟩, some "main", ⟨?_, ?_⟩, by decide⟩
  · intro _; rfl
  · intro _ r hr
    cases hr
    decide


/-! ### A GitLab source

In GitLab, CI_COMMIT_BRANCH and CI_COMMIT_TAG are not set in a merge request pipeline
(https://docs.gitlab.com/ci/variables/predefined_variables/, rows CI_COMMIT_BRANCH: "Not available in merge request
pipelines or tag pipelines", CI_COMMIT_TAG: "Available only in pipelines for tags"). `glEnv` is that environment.
Woodpecker holds the target branch in CI_COMMIT_BRANCH there (`wpMap`). A test of the branch ref therefore needs the
pull request test in front of it. -/

/-- A GitLab run: no branch and no tag in a merge request pipeline. -/
def glEnv (env : Env) : View
  | .branch => if env.event = some .pullRequest then none else mapEnv env .branch
  | .tag => if env.event = some .pullRequest then none else mapEnv env .tag
  | r => mapEnv env r

def evPrRx : E := .rx (.ref .event) .exact "merge_request_event"
def evNotPrRx : E := .nrx (.ref .event) .exact "merge_request_event"

/-- Mirrors expr.AssumeGitLab (expr/translate_facts.go): a test of the branch ref against a non-empty literal
gains the pull request test. A positive test is false on a pull request, so it is guarded by `event !~ pr`;
a negative one is true there, so it is joined by `event =~ pr`. A use of the branch ref in any other shape has
no rewrite (`none`). -/
def glPr : E → Option E
  | .not x => (glPr x).map .not
  | .and l r => (glPr l).bind fun a => (glPr r).map fun b => .and a b
  | .or l r => (glPr l).bind fun a => (glPr r).map fun b => .or a b
  | .rx (.ref .branch) m s => if s != "" then some (.and evNotPrRx (.rx (.ref .branch) m s)) else none
  | .nrx (.ref .branch) m s => if s != "" then some (.or evPrRx (.nrx (.ref .branch) m s)) else none
  | .eq (.ref .branch) (.str s) => if s != "" then some (.and evNotPrRx (.eq (.ref .branch) (.str s))) else none
  | .ne (.ref .branch) (.str s) => if s != "" then some (.or evPrRx (.ne (.ref .branch) (.str s))) else none
  | e => if (TranslateClass.refs e).all (fun r => r != .branch) then some e else none

theorem glEnv_wpMap (env : Env) (tgt : Option String) {r : Ref} (h : r ≠ .branch) : glEnv env r = wpMap env tgt r := by
  cases r <;> simp [glEnv, wpMap] at h ⊢

theorem evNotPrRx_eval (V : View) (hV : V .event = (mapEnv env) .event) :
    eval .gitlab V evNotPrRx = .bool (!decide (env.event = some .pullRequest)) := by
  cases h : env.event with
  | none => simp [evNotPrRx, eval, refV, hV, mapEnv, h, matchV, modeTest]
  | some e => cases e <;> simp [evNotPrRx, eval, refV, hV, mapEnv, h, evGl, matchV, modeTest, lower]

theorem evPrRx_eval (V : View) (hV : V .event = (mapEnv env) .event) :
    eval .gitlab V evPrRx = .bool (decide (env.event = some .pullRequest)) := by
  cases h : env.event with
  | none => simp [evPrRx, eval, refV, hV, mapEnv, h, matchV, modeTest]
  | some e => cases e <;> simp [evPrRx, eval, refV, hV, mapEnv, h, evGl, matchV, modeTest, lower]


theorem glPr_free {e e' : E} (env : Env) (tgt : Option String)
    (h : (TranslateClass.refs e).all (fun r => r != .branch) = true) (he : e' = e) :
    eval .gitlab (wpMap env tgt) e' = eval .gitlab (glEnv env) e := by
  subst he
  apply TranslateClass.eval_congr
  intro r hr
  have : r ≠ .branch := by
    have := List.all_eq_true.mp h r hr
    simpa using this
  exact (glEnv_wpMap env tgt this).symm

theorem branch_pr {env : Env} (tgt : Option String) (V1 V2 : View) (hp : env.event = some .pullRequest)
    (h1 : V1 .branch = tgt) (h2 : V2 .branch = none) (s : String) (hs : s ≠ "") (m : Mode) :
    matchV m s (refV V2 .branch) = false := by
  simp [refV, h2, matchV, lower_empty, Woodpecker.modeTest_empty m hs]

theorem glPr_default {e e' : E} (env : Env) (tgt : Option String)
    (h : (if (TranslateClass.refs e).all (fun r => r != .branch) = true then some e else none) = some e') :
    eval .gitlab (wpMap env tgt) e' = eval .gitlab (glEnv env) e := by
  split at h
  · rename_i hf
    cases h
    exact glPr_free env tgt hf rfl
  · simp at h

/-- A positive test of the branch ref: false on a pull request in GitLab, so the pull request test in front of it
keeps Woodpecker false there. -/
theorem glPr_pos (env : Env) (tgt : Option String) (T : E)
    (hT : ∀ hp : env.event = some .pullRequest, eval .gitlab (glEnv env) T = .bool false)
    (hn : ∀ hp : env.event ≠ some .pullRequest, eval .gitlab (wpMap env tgt) T = eval .gitlab (glEnv env) T) :
    eval .gitlab (wpMap env tgt) (.and evNotPrRx T) = eval .gitlab (glEnv env) T := by
  by_cases hp : env.event = some .pullRequest
  · have hl := evNotPrRx_eval (env := env) (wpMap env tgt) (by simp [wpMap, hp])
    simp [eval, hl, hp, logicalTruthy, rubyTruthy, hT hp]
  · have hl := evNotPrRx_eval (env := env) (wpMap env tgt) (by simp [wpMap, hp])
    simp [eval, hl, hp, logicalTruthy, rubyTruthy, hn hp]

theorem glPr_neg (env : Env) (tgt : Option String) (T : E)
    (hT : ∀ hp : env.event = some .pullRequest, eval .gitlab (glEnv env) T = .bool true)
    (hn : ∀ hp : env.event ≠ some .pullRequest, eval .gitlab (wpMap env tgt) T = eval .gitlab (glEnv env) T) :
    eval .gitlab (wpMap env tgt) (.or evPrRx T) = eval .gitlab (glEnv env) T := by
  by_cases hp : env.event = some .pullRequest
  · have hl := evPrRx_eval (env := env) (wpMap env tgt) (by simp [wpMap, hp])
    simp [eval, hl, hp, logicalTruthy, rubyTruthy, hT hp]
  · have hl := evPrRx_eval (env := env) (wpMap env tgt) (by simp [wpMap, hp])
    simp [eval, hl, hp, logicalTruthy, rubyTruthy, hn hp]

theorem branch_agree {env : Env} (tgt : Option String) (hp : env.event ≠ some .pullRequest) :
    wpMap env tgt .branch = glEnv env .branch := by simp [wpMap, glEnv, hp]

theorem branch_none {env : Env} (tgt : Option String) (hp : env.event = some .pullRequest) : glEnv env .branch = none := by
  simp [glEnv, hp]

/-- On every environment, the rewritten condition read on Woodpecker's environment has the value of the source
read on GitLab's: the pull request test replaces what the branch ref holds on a pull request. -/
theorem glPr_eval (env : Env) (tgt : Option String) :
    ∀ e e' : E, glPr e = some e' → eval .gitlab (wpMap env tgt) e' = eval .gitlab (glEnv env) e := by
  intro e
  induction e with
  | not x ih =>
    intro e' h
    simp only [glPr] at h
    obtain ⟨a, ha, rfl⟩ := Option.map_eq_some_iff.mp h
    simp [eval, ih a ha]
  | and l r ihl ihr =>
    intro e' h
    simp only [glPr] at h
    obtain ⟨a, ha, h2⟩ := Option.bind_eq_some_iff.mp h
    obtain ⟨b, hb, rfl⟩ := Option.map_eq_some_iff.mp h2
    simp [eval, ihl a ha, ihr b hb]
  | or l r ihl ihr =>
    intro e' h
    simp only [glPr] at h
    obtain ⟨a, ha, h2⟩ := Option.bind_eq_some_iff.mp h
    obtain ⟨b, hb, rfl⟩ := Option.map_eq_some_iff.mp h2
    simp [eval, ihl a ha, ihr b hb]
  | rx x m s _ =>
    intro e' h
    cases x
    case ref r0 _ =>
      cases r0
      case branch =>
        simp only [glPr] at h
        split at h
        · rename_i hs
          have hs' : s ≠ "" := by simpa using hs
          cases h
          apply glPr_pos
          · intro hp
            simp [eval, refV, branch_none tgt hp, matchV, lower_empty, Woodpecker.modeTest_empty m hs']
          · intro hp
            simp [eval, refV, branch_agree tgt hp]
        · simp at h
      all_goals (simp only [glPr] at h; exact glPr_default env tgt h)
    all_goals (simp only [glPr] at h; exact glPr_default env tgt h)
  | nrx x m s _ =>
    intro e' h
    cases x
    case ref r0 _ =>
      cases r0
      case branch =>
        simp only [glPr] at h
        split at h
        · rename_i hs
          have hs' : s ≠ "" := by simpa using hs
          cases h
          apply glPr_neg
          · intro hp
            simp [eval, refV, branch_none tgt hp, matchV, lower_empty, Woodpecker.modeTest_empty m hs']
          · intro hp
            simp [eval, refV, branch_agree tgt hp]
        · simp at h
      all_goals (simp only [glPr] at h; exact glPr_default env tgt h)
    all_goals (simp only [glPr] at h; exact glPr_default env tgt h)
  | eq l r _ _ =>
    intro e' h
    cases l
    case ref x0 _ =>
      cases x0
      case branch =>
        cases r
        case str s _ =>
          simp only [glPr] at h
          split at h
          · rename_i hs
            have hs' : s ≠ "" := by simpa using hs
            cases h
            apply glPr_pos
            · intro hp
              simp [eval, refV, branch_none tgt hp, eqV]
            · intro hp
              simp [eval, refV, branch_agree tgt hp]
          · simp at h
        all_goals (simp only [glPr] at h; try exact glPr_default env tgt h)
      all_goals (simp only [glPr] at h; exact glPr_default env tgt h)
    all_goals (simp only [glPr] at h; exact glPr_default env tgt h)
  | ne l r _ _ =>
    intro e' h
    cases l
    case ref x0 _ =>
      cases x0
      case branch =>
        cases r
        case str s _ =>
          simp only [glPr] at h
          split at h
          · rename_i hs
            have hs' : s ≠ "" := by simpa using hs
            cases h
            apply glPr_neg
            · intro hp
              simp [eval, refV, branch_none tgt hp, eqV]
            · intro hp
              simp [eval, refV, branch_agree tgt hp]
          · simp at h
        all_goals (simp only [glPr] at h; try exact glPr_default env tgt h)
      all_goals (simp only [glPr] at h; exact glPr_default env tgt h)
    all_goals (simp only [glPr] at h; exact glPr_default env tgt h)
  | _ =>
    intro e' h
    simp only [glPr] at h
    exact glPr_default env tgt h


theorem glSub_pr : Woodpecker.GlSub evPrRx = true ∧ Woodpecker.GlSub evNotPrRx = true := by
  simp [evPrRx, evNotPrRx, Woodpecker.GlSub]

/-- The rewrite stays in the subset Woodpecker evaluates as the GitLab class does. -/
theorem glPr_glSub : ∀ e e' : E, Woodpecker.GlSub e = true → glPr e = some e' → Woodpecker.GlSub e' = true := by
  intro e
  induction e with
  | not x ih =>
    intro e' hg h
    simp only [glPr] at h
    obtain ⟨a, ha, rfl⟩ := Option.map_eq_some_iff.mp h
    exact ih a (by simpa [Woodpecker.GlSub] using hg) ha
  | and l r ihl ihr =>
    intro e' hg h
    simp only [Woodpecker.GlSub, Bool.and_eq_true] at hg
    simp only [glPr] at h
    obtain ⟨a, ha, h2⟩ := Option.bind_eq_some_iff.mp h
    obtain ⟨b, hb, rfl⟩ := Option.map_eq_some_iff.mp h2
    simp [Woodpecker.GlSub, ihl a hg.1 ha, ihr b hg.2 hb]
  | or l r ihl ihr =>
    intro e' hg h
    simp only [Woodpecker.GlSub, Bool.and_eq_true] at hg
    simp only [glPr] at h
    obtain ⟨a, ha, h2⟩ := Option.bind_eq_some_iff.mp h
    obtain ⟨b, hb, rfl⟩ := Option.map_eq_some_iff.mp h2
    simp [Woodpecker.GlSub, ihl a hg.1 ha, ihr b hg.2 hb]
  | rx x m s _ =>
    intro e' hg h
    have hs : s ≠ "" := by
      cases x <;> simp [Woodpecker.GlSub] at hg <;> exact hg
    cases x
    case ref r0 _ =>
      cases r0 <;> (simp only [glPr] at h; split at h <;> first | (cases h; simp [Woodpecker.GlSub, evNotPrRx, evPrRx, hs, hg]; done) | (cases h; exact hg) | simp at h)
    all_goals (simp only [glPr] at h; split at h <;> first | (cases h; exact hg) | simp at h)
  | nrx x m s _ =>
    intro e' hg h
    have hs : s ≠ "" := by
      cases x <;> simp [Woodpecker.GlSub] at hg <;> exact hg
    cases x
    case ref r0 _ =>
      cases r0 <;> (simp only [glPr] at h; split at h <;> first | (cases h; simp [Woodpecker.GlSub, evNotPrRx, evPrRx, hs, hg]; done) | (cases h; exact hg) | simp at h)
    all_goals (simp only [glPr] at h; split at h <;> first | (cases h; exact hg) | simp at h)
  | eq l r _ _ =>
    intro e' hg h
    cases l
    case ref x0 _ =>
      cases x0 <;> cases r <;> (simp only [glPr] at h; split at h <;> first | (cases h; simp [Woodpecker.GlSub, evNotPrRx, evPrRx] at hg ⊢; simp_all; done) | (cases h; exact hg) | simp at h)
    all_goals (simp only [glPr] at h; split at h <;> first | (cases h; exact hg) | simp at h)
  | ne l r _ _ =>
    intro e' hg h
    cases l
    case ref x0 _ =>
      cases x0 <;> cases r <;> (simp only [glPr] at h; split at h <;> first | (cases h; simp [Woodpecker.GlSub, evNotPrRx, evPrRx] at hg ⊢; simp_all; done) | (cases h; exact hg) | simp at h)
    all_goals (simp only [glPr] at h; split at h <;> first | (cases h; exact hg) | simp at h)
  | _ =>
    intro e' hg h
    simp only [glPr] at h
    split at h <;> first | (cases h; exact hg) | simp at h


/-- A GitLab condition in the subset, rewritten by `glPr`, has on Woodpecker (an unset name reads "") the
truthiness it has in GitLab, on every run, including a pull request run where Woodpecker holds the target
branch in CI_COMMIT_BRANCH. -/
theorem glWp_sound (env : Env) (tgt : Option String) {e e' : E} (hg : Woodpecker.GlSub e = true)
    (h : glPr e = some e') :
    holds .gitlab (Woodpecker.wpView (wpMap env tgt)) e' = holds .gitlab (glEnv env) e := by
  have hs := glPr_glSub e e' hg h
  unfold holds
  rw [Woodpecker.eval_woodpecker (wpMap env tgt) (Woodpecker.wpView (wpMap env tgt)) e' hs
    (fun r _ => rfl), glPr_eval env tgt e e' h]

/-- Why the pull request test exists: the plain translation of a branch test reads the target branch on a pull
request, where GitLab has no branch. -/
theorem glWp_plain_unsound : ∃ (env : Env) (tgt : Option String),
    holds .gitlab (Woodpecker.wpView (wpMap env tgt)) (.rx (.ref .branch) .exact "main") ≠
      holds .gitlab (glEnv env) (.rx (.ref .branch) .exact "main") := by
  refine ⟨⟨some "feature", some .branch, some .pullRequest, none, none⟩, some "main", by decide +kernel⟩

/-- The same for a negative test, which is true in GitLab on a pull request. -/
theorem glWp_plain_unsound_neg : ∃ (env : Env) (tgt : Option String),
    holds .gitlab (Woodpecker.wpView (wpMap env tgt)) (.nrx (.ref .branch) .exact "main") ≠
      holds .gitlab (glEnv env) (.nrx (.ref .branch) .exact "main") := by
  refine ⟨⟨some "feature", some .branch, some .pullRequest, none, none⟩, some "main", by decide +kernel⟩


/-! ### The other pairs

Each theorem is the run on which a plain translation of one pair holds where its source does not. The Go rules that
refuse these shapes (expr/translate_facts.go: route) are checked by tests, not proved. -/

/-- A merge request into main from `feature`, as Woodpecker and GHA report it. -/
def prEnv : Env := ⟨some "7/merge", some .branch, some .pullRequest, some "abc", none⟩

/-- GHA to GitLab: `!(ref_type == 'branch' && contains(ref_name, 'merge'))` becomes `!(branch =~ /merge/i)`, which is
true in a merge request pipeline where GHA ref_name `7/merge` makes the source false. -/
theorem ghaGl_neg_affix_unsound :
    holds .gha (ghaView prEnv)
      (.not (.and (.eq (.ref .refType) (.str "branch")) (.contains (.ref .refName) (.str "merge")))) = false ∧
    holds .gitlab (glEnv prEnv) (.not (.rx (.ref .branch) .sub "merge")) = true := by
  decide +kernel

/-- GitLab to GHA: `branch =~ /merge/i` is false in a merge request pipeline and its GHA form is true on `7/merge`. -/
theorem glGha_affix_unsound :
    holds .gitlab (glEnv prEnv) (.rx (.ref .branch) .sub "merge") = false ∧
    holds .gha (ghaView prEnv) (.and (.eq (.ref .refType) (.str "branch")) (.contains (.ref .refName) (.str "merge"))) = true := by
  decide +kernel

/-- Woodpecker to GitLab: `branch !~ /^main$/i` is false on a pull request into main and true in GitLab, where
the branch is unset. -/
theorem wpGl_neg_unsound :
    holds .gitlab (Woodpecker.wpView (wpMap prEnv (some "main"))) (.nrx (.ref .branch) .exact "main") = false ∧
    holds .gitlab (glEnv prEnv) (.nrx (.ref .branch) .exact "main") = true := by
  decide +kernel

/-- Woodpecker to GHA: the same condition becomes `!(ref_type == 'branch' && ref_name == 'main')`, true on `7/merge`. -/
theorem wpGha_neg_unsound :
    holds .gitlab (Woodpecker.wpView (wpMap prEnv (some "main"))) (.nrx (.ref .branch) .exact "main") = false ∧
    holds .gha (ghaView prEnv) (.not (.and (.eq (.ref .refType) (.str "branch")) (.eq (.ref .refName) (.str "main")))) = true := by
  decide +kernel

end TranslateFacts
end Pipemaster
