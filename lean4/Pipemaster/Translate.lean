import Pipemaster.Expr

namespace Pipemaster
namespace Translate

open Pipemaster.Expr

/-! Translation between the GHA and GitLab expression dialects (expr.Translate, expr/translate.go).
It is exact: the target gives the same truthiness as the source on every environment. -/

/-- A literal the case-insensitive regex can stand for: non-empty and ASCII (the model lowers ASCII only),
with no backslash (GitLab patterns cannot spell a trailing one and read `\/` as a slash). -/
def okLit (s : String) : Bool := s != "" && s.toList.all fun c => c.toNat < 128 && c != '\\'

/-- A literal that GHA could coerce to a number: it starts with a digit or a minus sign. Against a
null value GHA reads `null == '0'` as true, which no regex reproduces. -/
def numLike (s : String) : Bool :=
  match s.toList with
  | c :: _ => c.isDigit || c == '-'
  | [] => false

def okEq (s : String) : Bool := okLit s && !numLike s

/-- GitLab rejects single-character patterns (`/./` is an `invalid expression syntax` error), so a
substring test needs at least two characters. -/
def okAff (m : Mode) (s : String) : Bool := okLit s && (m != .sub || decide (2 ≤ s.length))

def evOfGha (l : String) : Option Ev :=
  if l = "push" then some .push
  else if l = "pull_request" then some .pullRequest
  else if l = "schedule" then some .schedule
  else if l = "workflow_dispatch" then some .workflowDispatch
  else none

def evOfGl (l : String) : Option Ev :=
  if l = "push" then some .push
  else if l = "merge_request_event" then some .pullRequest
  else if l = "schedule" then some .schedule
  else if l = "web" then some .workflowDispatch
  else none

def rtRef : RT → Ref
  | .branch => .branch
  | .tag => .tag

/-- `ref_type == 'branch'` or `'tag'` in either operand order (GHA compares case-insensitively). -/
def typeGuard : E → Option RT
  | .eq (.ref .refType) (.str s) | .eq (.str s) (.ref .refType) =>
    if lower s = "branch" then some .branch else if lower s = "tag" then some .tag else none
  | _ => none

/-- Expressions whose value is always a boolean; GitLab tests the left operand of && and || with Ruby
truthiness, which only agrees with GHA's on booleans. -/
def bv : E → Bool
  | .not _ | .eq _ _ | .ne _ _ | .startsWith _ _ | .endsWith _ _ | .contains _ _
  | .rx _ _ _ | .nrx _ _ _ => true
  | .and l r | .or l r => bv l && bv r
  | _ => false

/-! ### GHA to GitLab -/

def rxNode (neg : Bool) (x : E) (m : Mode) (s : String) : E :=
  if neg then .nrx x m s else .rx x m s

def trCmp (neg : Bool) (r : Ref) (s : String) : Option E :=
  match r with
  | .event => (evOfGha (lower s)).map fun e => rxNode neg (.ref .event) .exact (evGl e)
  | .refName | .sha => if okEq s then some (rxNode neg (.ref r) .exact s) else none
  | _ => none

def trAff (m : Mode) (r : Ref) (s : String) : Option E :=
  match r with
  | .refName | .sha => if okAff m s then some (.rx (.ref r) m s) else none
  | _ => none

def trEq (neg : Bool) : E → E → Option E
  | .ref r, .str s | .str s, .ref r => trCmp neg r s
  | _, _ => none

def trAffE (m : Mode) : E → E → Option E
  | .ref r, .str s => trAff m r s
  | _, _ => none

/-- Mirrors expr.Translate (expr/translate.go) from gha to gitlab. -/
def tr : E → Option E
  | .ref .refName => some (.ref .refName)
  | .ref .sha => some (.ref .sha)
  | .ref .event => some (.ref .event)
  | .eq l r => trEq false l r
  | .ne l r => trEq true l r
  | .startsWith a b => trAffE .prefix a b
  | .endsWith a b => trAffE .suffix a b
  | .contains a b => trAffE .sub a b
  | .not x => (tr x).map .not
  | .and l r =>
    match typeGuard l, tr r with
    | some t, some (.rx (.ref .refName) m s) => some (.rx (.ref (rtRef t)) m s)
    | _, _ => if bv l then (tr l).bind fun a => (tr r).map fun b => .and a b else none
  | .or l r => if bv l then (tr l).bind fun a => (tr r).map fun b => .or a b else none
  | _ => none

/-! ### The exact subset, stated without reference to `tr` -/

def cmpOk (r : Ref) (s : String) : Bool :=
  match r with
  | .event => (evOfGha (lower s)).isSome
  | .refName | .sha => okEq s
  | _ => false

def affOk (m : Mode) (r : Ref) (s : String) : Bool :=
  match r with
  | .refName | .sha => okAff m s
  | _ => false

def eqOk : E → E → Bool
  | .ref r, .str s | .str s, .ref r => cmpOk r s
  | _, _ => false

def affEOk (m : Mode) : E → E → Bool
  | .ref r, .str s => affOk m r s
  | _, _ => false

/-- A positive test of ref_name that the branch/tag guard can fold into one regex on CI_COMMIT_BRANCH. -/
def nameTest : E → Bool
  | .eq (.ref .refName) (.str s) | .eq (.str s) (.ref .refName) => okEq s
  | .startsWith (.ref .refName) (.str s) => okAff .prefix s
  | .endsWith (.ref .refName) (.str s) => okAff .suffix s
  | .contains (.ref .refName) (.str s) => okAff .sub s
  | _ => false

/-- The GHA expressions that translate to GitLab (Mirrors expr.Translatable, expr/translate.go). -/
def trOk : E → Bool
  | .ref r => r == .refName || r == .sha || r == .event
  | .eq l r | .ne l r => eqOk l r
  | .startsWith a b => affEOk .prefix a b
  | .endsWith a b => affEOk .suffix a b
  | .contains a b => affEOk .sub a b
  | .not x => trOk x
  | .and l r => ((typeGuard l).isSome && nameTest r) || (bv l && trOk l && trOk r)
  | .or l r => bv l && trOk l && trOk r
  | _ => false

def Translatable (e : E) : Prop := trOk e = true

theorem trCmp_isSome (neg : Bool) (r : Ref) (s : String) :
    (trCmp neg r s).isSome = cmpOk r s := by
  cases r <;> simp [trCmp, cmpOk] <;> split <;> simp_all

theorem trAff_isSome (m : Mode) (r : Ref) (s : String) :
    (trAff m r s).isSome = affOk m r s := by
  cases r <;> simp [trAff, affOk] <;> split <;> simp_all

theorem tr_and_out {l r e' : E} (h : tr (.and l r) = some e') :
    (∃ t m s, e' = .rx (.ref (rtRef t)) m s) ∨ ∃ a b, e' = .and a b := by
  simp only [tr] at h
  split at h
  · rename_i t m s _ _
    cases h; exact Or.inl ⟨t, m, s, rfl⟩
  · split at h
    · rcases Option.bind_eq_some_iff.mp h with ⟨a, _, h2⟩
      rcases Option.map_eq_some_iff.mp h2 with ⟨b, _, h3⟩
      exact Or.inr ⟨a, b, h3.symm⟩
    · simp at h

theorem tr_rx_name {r : E} {m : Mode} {s : String}
    (h : tr r = some (.rx (.ref .refName) m s)) : nameTest r = true := by
  cases r with
  | eq l r' =>
    simp only [tr, trEq] at h
    split at h
    · rename_i x s'
      cases x <;> simp [trCmp, rxNode] at h
      obtain ⟨h1, -, rfl⟩ := h
      simp [nameTest, h1]
    · rename_i s' x
      cases x <;> simp [trCmp, rxNode] at h
      obtain ⟨h1, -, rfl⟩ := h
      simp [nameTest, h1]
    · simp at h
  | ne l r' =>
    simp only [tr, trEq] at h
    split at h
    · rename_i x s'
      cases x <;> simp [trCmp, rxNode] at h <;> split at h <;> simp at h
    · rename_i s' x
      cases x <;> simp [trCmp, rxNode] at h <;> split at h <;> simp at h
    · simp at h
  | startsWith a b =>
    simp only [tr, trAffE] at h
    split at h
    · rename_i x s'
      cases x <;> simp [trAff] at h
      obtain ⟨h1, -, rfl⟩ := h
      simp [nameTest, h1]
    · simp at h
  | endsWith a b =>
    simp only [tr, trAffE] at h
    split at h
    · rename_i x s'
      cases x <;> simp [trAff] at h
      obtain ⟨h1, -, rfl⟩ := h
      simp [nameTest, h1]
    · simp at h
  | contains a b =>
    simp only [tr, trAffE] at h
    split at h
    · rename_i x s'
      cases x <;> simp [trAff] at h
      obtain ⟨h1, -, rfl⟩ := h
      simp [nameTest, h1]
    · simp at h
  | and l r' =>
    rcases tr_and_out h with ⟨t, m', s', h'⟩ | ⟨a, b, h'⟩
    · cases t <;> simp [rtRef] at h'
    · simp at h'
  | or l r' =>
    simp only [tr] at h
    split at h
    · rcases Option.bind_eq_some_iff.mp h with ⟨a, _, h2⟩
      rcases Option.map_eq_some_iff.mp h2 with ⟨b, _, h3⟩
      simp at h3
    · simp at h
  | not x =>
    simp only [tr] at h
    rcases Option.map_eq_some_iff.mp h with ⟨a, _, h3⟩
    simp at h3
  | ref r => cases r <;> simp [tr] at h
  | null => simp [tr] at h
  | bool b => simp [tr] at h
  | str b => simp [tr] at h
  | num b => simp [tr] at h
  | lt a b => simp [tr] at h
  | call a b => simp [tr] at h
  | rx a b c => simp [tr] at h
  | nrx a b c => simp [tr] at h
  | rawRx a b => simp [tr] at h
  | opq => simp [tr] at h

theorem nameTest_tr {r : E} (h : nameTest r = true) :
    ∃ m s, tr r = some (.rx (.ref .refName) m s) := by
  cases r <;> simp only [nameTest] at h <;> (try split at h) <;> try (simp at h; done)
  all_goals rename_i _ _ _ s heq
  all_goals first
    | (simp at heq; done)
    | (simp at heq; obtain ⟨rfl, rfl⟩ := heq
       first
        | (refine ⟨.exact, s, ?_⟩; simp [tr, trEq, trCmp, rxNode, h]; done)
        | (refine ⟨.prefix, s, ?_⟩; simp [tr, trAffE, trAff, h]; done)
        | (refine ⟨.suffix, s, ?_⟩; simp [tr, trAffE, trAff, h]; done)
        | (refine ⟨.sub, s, ?_⟩; simp [tr, trAffE, trAff, h]; done))

theorem opt_any (o : Option E) (c : Bool) : Option.any (fun _ => c) o = (o.isSome && c) := by
  cases o <;> simp

theorem trEq_isSome (neg : Bool) (l r : E) : (trEq neg l r).isSome = eqOk l r := by
  unfold trEq eqOk
  split <;> simp [trCmp_isSome]

theorem trAffE_isSome (m : Mode) (a b : E) : (trAffE m a b).isSome = affEOk m a b := by
  unfold trAffE affEOk
  split <;> simp [trAff_isSome]

theorem tr_and_generic {l r : E} (hnr : ∀ t m s, typeGuard l = some t → tr r ≠ some (.rx (.ref .refName) m s)) :
    (tr (.and l r)).isSome = (bv l && (tr l).isSome && (tr r).isSome) := by
  simp only [tr]
  split
  · rename_i hb
    simp [hb, Option.isSome_bind, opt_any, Option.isSome_map]
  · rename_i hb
    simp [hb]

theorem tr_isSome (e : E) : (tr e).isSome = trOk e := by
  induction e with
  | ref r => cases r <;> simp [tr, trOk]
  | eq l r => simp [tr, trOk, trEq_isSome]
  | ne l r => simp [tr, trOk, trEq_isSome]
  | startsWith a b => simp [tr, trOk, trAffE_isSome]
  | endsWith a b => simp [tr, trOk, trAffE_isSome]
  | contains a b => simp [tr, trOk, trAffE_isSome]
  | not x ih => simp [tr, trOk, ← ih]
  | or l r ihl ihr =>
    simp only [tr, trOk]
    by_cases hb : bv l = true <;> simp [hb, ← ihl, ← ihr, Option.isSome_bind, Option.isSome_map, opt_any, Bool.and_assoc]
  | and l r ihl ihr =>
    by_cases hg : (typeGuard l).isSome = true
    · obtain ⟨t, ht⟩ := Option.isSome_iff_exists.mp hg
      by_cases hn : nameTest r = true
      · obtain ⟨m, s, h⟩ := nameTest_tr hn
        simp [tr, trOk, ht, h, hn]
      · rw [tr_and_generic (fun t' m s h1 h2 => hn (tr_rx_name h2)), ihl, ihr]
        simp [trOk, hn, Bool.and_assoc]
    · have hg' : typeGuard l = none := by simpa using hg
      rw [tr_and_generic (by simp [hg']), ihl, ihr]
      simp [trOk, hg, Bool.and_assoc]
  | _ => simp [tr, trOk]

/-- Translation fails exactly outside the translatable subset. -/
theorem tr_none_iff (e : E) : tr e = none ↔ ¬ Translatable e := by
  have h := tr_isSome e
  unfold Translatable
  cases hx : tr e <;> simp_all

theorem eqV_comm (a b : Val) : eqV .gha a b = eqV .gha b a := by
  cases a <;> cases b <;> simp [eqV, Bool.beq_comm]

theorem lower_empty : lower "" = "" := by decide

theorem evOfGha_name {l : String} {e : Ev} (h : evOfGha l = some e) : l = evGha e := by
  unfold evOfGha at h
  split_ifs at h <;> simp at h <;> subst h <;> simp_all [evGha]

theorem evGha_lower (e : Ev) : lower (evGha e) = evGha e := by cases e <;> decide

theorem ev_agree (e e0 : Ev) :
    (lower (evGl e) == lower (evGl e0)) = (lower (evGha e) == evGha e0) := by
  cases e <;> cases e0 <;> decide

theorem match_exact_eq {s : String} (hs : (s == "") = false) (w : View) (r : Ref) :
    matchV .exact s (refV w r) = eqV .gha (refV w r) (.str s) := by
  cases h : w r <;> simp [refV, matchV, eqV, modeTest, hs, h]

def negIf (neg b : Bool) : Bool := if neg then !b else b

theorem eval_rxNode (neg : Bool) (v : View) (x : E) (m : Mode) (s : String) :
    eval .gitlab v (rxNode neg x m s) = .bool (negIf neg (matchV m s (eval .gitlab v x))) := by
  cases neg <;> simp [rxNode, negIf, eval]

theorem trCmp_eval {neg : Bool} {r : Ref} {s : String} {e' : E} (env : Env)
    (h : trCmp neg r s = some e') :
    eval .gitlab (mapEnv env) e' = .bool (negIf neg (eqV .gha (refV (ghaView env) r) (.str s))) := by
  cases r <;> simp only [trCmp] at h
  case event =>
    obtain ⟨e0, he0, rfl⟩ := Option.map_eq_some_iff.mp h
    have hl := evOfGha_name he0
    have hs : (s == "") = false := by
      by_contra hc
      have : s = "" := by simpa using hc
      subst this
      simp [lower_empty, evGha] at hl
      cases e0 <;> simp [evGha] at hl
    rw [eval_rxNode]
    congr 2
    cases ho : env.event with
    | none => simp [eval, refV, mapEnv, ghaView, matchV, eqV, ho, hs]
    | some e =>
      simp only [eval, refV, mapEnv, ghaView, ho, Option.map_some, matchV, modeTest, eqV]
      rw [ev_agree e e0, ← hl]
  case refName | sha =>
    split at h
    · rename_i hk
      cases h
      have hs : (s == "") = false := by
        simp [okEq, okLit] at hk
        simpa using hk.1.1
      rw [eval_rxNode]
      congr 2
      first
        | simpa [eval, mapEnv, ghaView, refV] using match_exact_eq hs (ghaView env) .refName
        | simpa [eval, mapEnv, ghaView, refV] using match_exact_eq hs (ghaView env) .sha
    · simp at h
  all_goals simp at h

theorem trAff_eval {m : Mode} {r : Ref} {s : String} {e' : E} (env : Env)
    (h : trAff m r s = some e') :
    eval .gitlab (mapEnv env) e' = .bool (affixV m (refV (ghaView env) r) (.str s)) := by
  cases r <;> simp only [trAff] at h
  case refName | sha =>
    split at h
    · cases h
      first
        | (cases ho : env.refName <;> simp [eval, refV, matchV, affixV, mapEnv, ghaView, ho]; done)
        | (cases ho : env.sha <;> simp [eval, refV, matchV, affixV, mapEnv, ghaView, ho])
    · simp at h
  all_goals simp at h

/-- The soundness invariant: equal truthiness, and equal boolean values on boolean-valued sources. -/
def Q (env : Env) (e e' : E) : Prop :=
  holds .gitlab (mapEnv env) e' = holds .gha (ghaView env) e ∧
  (bv e = true → eval .gitlab (mapEnv env) e' = .bool (holds .gha (ghaView env) e))

theorem Q_of_bool {env : Env} {e e' : E} {b : Bool}
    (h1 : eval .gitlab (mapEnv env) e' = .bool b) (h2 : eval .gha (ghaView env) e = .bool b) :
    Q env e e' := by
  simp [Q, holds, h1, h2, truthy]

theorem trEq_Q {neg : Bool} {l r e' : E} (env : Env) (h : trEq neg l r = some e') :
    ∃ b, eval .gitlab (mapEnv env) e' = .bool b ∧
      eval .gha (ghaView env) (if neg then E.ne l r else E.eq l r) = .bool b := by
  unfold trEq at h
  split at h
  · rename_i r0 s0
    refine ⟨_, trCmp_eval env h, ?_⟩
    cases neg <;> simp [negIf, eval]
  · rename_i s0 r0
    refine ⟨_, trCmp_eval env h, ?_⟩
    cases neg <;> simp [negIf, eval, eqV_comm (refV (ghaView env) r0)]
  · simp at h

theorem trAffE_Q {m : Mode} {a b e' : E} (env : Env) (hm : m ≠ .exact) (h : trAffE m a b = some e') :
    ∃ x, eval .gitlab (mapEnv env) e' = .bool x ∧
      (eval .gha (ghaView env) (.startsWith a b) = .bool x ∧ m = .prefix ∨
       eval .gha (ghaView env) (.endsWith a b) = .bool x ∧ m = .suffix ∨
       eval .gha (ghaView env) (.contains a b) = .bool x ∧ m = .sub) := by
  unfold trAffE at h
  split at h
  · rename_i r0 s0
    refine ⟨_, trAff_eval env h, ?_⟩
    cases m <;> simp [eval] <;> exact absurd rfl hm
  · simp at h

theorem holds_bool (sem : Sem) (v : View) (e : E) (b : Bool) (h : eval sem v e = .bool b) :
    holds sem v e = b := by simp [holds, h, truthy]

theorem Q_not {env : Env} {x a : E} (h : Q env x a) : Q env (.not x) (.not a) := by
  have h1 : eval .gitlab (mapEnv env) (.not a) = .bool (!holds .gitlab (mapEnv env) a) := rfl
  have h2 : eval .gha (ghaView env) (.not x) = .bool (!holds .gha (ghaView env) x) := rfl
  exact Q_of_bool (b := !holds .gha (ghaView env) x) (by rw [h1, h.1]) h2

theorem Q_or {env : Env} {l r a b : E} (hbl : bv l = true) (hl : Q env l a) (hr : Q env r b) :
    Q env (.or l r) (.or a b) := by
  have e1 := hl.2 hbl
  have hl1 : truthy (eval .gha (ghaView env) l) = holds .gha (ghaView env) l := rfl
  by_cases h : holds .gha (ghaView env) l = true
  · have g : eval .gitlab (mapEnv env) (.or a b) = .bool true := by simp [eval, logicalTruthy, rubyTruthy, e1, h]
    have g2 : holds .gha (ghaView env) (.or l r) = true := by
      simp [holds, eval, logicalTruthy, ← hl1] at h ⊢; simp [h]
    refine ⟨?_, fun _ => ?_⟩
    · rw [holds_bool _ _ _ _ g, g2]
    · rw [g, g2]
  · have h' : holds .gha (ghaView env) l = false := by simpa using h
    have g : eval .gitlab (mapEnv env) (.or a b) = eval .gitlab (mapEnv env) b := by
      simp [eval, logicalTruthy, rubyTruthy, e1, h']
    have g2 : eval .gha (ghaView env) (.or l r) = eval .gha (ghaView env) r := by
      simp [eval, logicalTruthy, ← hl1] at h' ⊢; simp [h']
    refine ⟨?_, fun hb => ?_⟩
    · simp only [holds, g, g2]; exact hr.1
    · have hbr : bv r = true := by simpa [bv, hbl] using hb
      rw [g]; simpa [holds, g2] using hr.2 hbr

theorem Q_and {env : Env} {l r a b : E} (hbl : bv l = true) (hl : Q env l a) (hr : Q env r b) :
    Q env (.and l r) (.and a b) := by
  have e1 := hl.2 hbl
  have hl1 : truthy (eval .gha (ghaView env) l) = holds .gha (ghaView env) l := rfl
  by_cases h : holds .gha (ghaView env) l = true
  · have g : eval .gitlab (mapEnv env) (.and a b) = eval .gitlab (mapEnv env) b := by
      simp [eval, logicalTruthy, rubyTruthy, e1, h]
    have g2 : eval .gha (ghaView env) (.and l r) = eval .gha (ghaView env) r := by
      simp [eval, logicalTruthy, ← hl1] at h ⊢; simp [h]
    refine ⟨?_, fun hb => ?_⟩
    · simp only [holds, g, g2]; exact hr.1
    · have hbr : bv r = true := by simpa [bv, hbl] using hb
      rw [g]; simpa [holds, g2] using hr.2 hbr
  · have h' : holds .gha (ghaView env) l = false := by simpa using h
    have g : eval .gitlab (mapEnv env) (.and a b) = .bool false := by
      simp [eval, logicalTruthy, rubyTruthy, e1, h']
    have g2 : holds .gha (ghaView env) (.and l r) = false := by
      simp [holds, eval, logicalTruthy, ← hl1] at h' ⊢; simp [h']
    refine ⟨?_, fun _ => ?_⟩
    · rw [holds_bool _ _ _ _ g, g2]
    · rw [g, g2]

theorem lower_rt (t : RT) : lower (rtName t) = rtName t := by cases t <;> decide

theorem guard_name {s0 : String} {t : RT} (h : (if lower s0 = "branch" then some RT.branch
    else if lower s0 = "tag" then some RT.tag else none) = some t) : lower s0 = rtName t := by
  split_ifs at h with h1 h2
  · cases h; simpa [rtName] using h1
  · cases h; simpa [rtName] using h2

theorem typeGuard_eval {l : E} {t : RT} (env : Env) (hg : typeGuard l = some t) :
    eval .gha (ghaView env) l = .bool (decide (env.refType = some t)) := by
  unfold typeGuard at hg
  split at hg
  all_goals first
    | (simp at hg; done)
    | (rename_i s0
       have hn := guard_name hg
       have hne : (s0 == "") = false := by
         by_contra hc
         have : s0 = "" := by simpa using hc
         subst this
         simp [lower_empty] at hn
         cases t <;> simp [rtName] at hn
       cases ho : env.refType with
       | none => simp [eval, refV, ghaView, ho, eqV, hne]
       | some t' =>
         simp only [eval, refV, ghaView, ho, Option.map_some, eqV, hn, lower_rt]
         cases t <;> cases t' <;> simp [rtName])

theorem shape_Q {env : Env} {l r : E} {t : RT} {m : Mode} {s : String}
    (hg : typeGuard l = some t) (hr : Q env r (.rx (.ref .refName) m s)) :
    Q env (.and l r) (.rx (.ref (rtRef t)) m s) := by
  have hrr : matchV m s (refV (mapEnv env) .refName) = holds .gha (ghaView env) r := by
    simpa [holds, eval, truthy] using hr.1
  have hl := typeGuard_eval env hg
  have key : matchV m s (refV (mapEnv env) (rtRef t)) = holds .gha (ghaView env) (.and l r) := by
    have hv : refV (mapEnv env) (rtRef t) =
        if env.refType = some t then refV (mapEnv env) .refName else .null := by
      by_cases h : env.refType = some t <;> cases t <;> simp [rtRef, mapEnv, refV, h]
    by_cases h : env.refType = some t
    · have h1 : holds .gha (ghaView env) (.and l r) = holds .gha (ghaView env) r := by
        simp [holds, eval, hl, h, logicalTruthy, truthy]
      rw [hv, if_pos h, h1, hrr]
    · have h1 : holds .gha (ghaView env) (.and l r) = false := by
        simp [holds, eval, hl, h, logicalTruthy, truthy]
      rw [hv, if_neg h, h1]
      rfl
  have e1 : eval .gitlab (mapEnv env) (.rx (.ref (rtRef t)) m s) = .bool (matchV m s (refV (mapEnv env) (rtRef t))) := by
    simp [eval]
  refine ⟨?_, fun _ => ?_⟩
  · rw [holds_bool _ _ _ _ e1, key]
  · rw [e1, key]

theorem tr_sound_aux (env : Env) : ∀ e e', tr e = some e' → Q env e e' := by
  intro e
  induction e with
  | ref r =>
    intro e' h
    cases r <;> simp only [tr, Option.some.injEq] at h <;> try (simp at h; done)
    all_goals subst h
    all_goals first
      | (refine ⟨?_, by simp [bv]⟩
         simp [holds, eval, refV, mapEnv, ghaView]
         done)
      | (refine ⟨?_, by simp [bv]⟩
         cases ho : env.event <;> simp [holds, eval, refV, mapEnv, ghaView, ho, truthy, evGl, evGha]
         · rename_i e0; cases e0 <;> simp [evGl, evGha])
  | eq l r =>
    intro e' h
    obtain ⟨b, h1, h2⟩ := trEq_Q env (neg := false) (by simpa [tr] using h)
    exact Q_of_bool h1 (by simpa using h2)
  | ne l r =>
    intro e' h
    obtain ⟨b, h1, h2⟩ := trEq_Q env (neg := true) (by simpa [tr] using h)
    exact Q_of_bool h1 (by simpa using h2)
  | startsWith a b =>
    intro e' h
    obtain ⟨x, h1, h2 | h2 | h2⟩ := trAffE_Q (m := .prefix) env (by decide) (by simpa [tr] using h)
    · exact Q_of_bool h1 h2.1
    · exact absurd h2.2 (by decide)
    · exact absurd h2.2 (by decide)
  | endsWith a b =>
    intro e' h
    obtain ⟨x, h1, h2 | h2 | h2⟩ := trAffE_Q (m := .suffix) env (by decide) (by simpa [tr] using h)
    · exact absurd h2.2 (by decide)
    · exact Q_of_bool h1 h2.1
    · exact absurd h2.2 (by decide)
  | contains a b =>
    intro e' h
    obtain ⟨x, h1, h2 | h2 | h2⟩ := trAffE_Q (m := .sub) env (by decide) (by simpa [tr] using h)
    · exact absurd h2.2 (by decide)
    · exact absurd h2.2 (by decide)
    · exact Q_of_bool h1 h2.1
  | not x ih =>
    intro e' h
    simp only [tr] at h
    obtain ⟨a, ha, rfl⟩ := Option.map_eq_some_iff.mp h
    exact Q_not (ih a ha)
  | or l r ihl ihr =>
    intro e' h
    simp only [tr] at h
    split at h
    · rename_i hb
      obtain ⟨a, ha, h2⟩ := Option.bind_eq_some_iff.mp h
      obtain ⟨b, hb', rfl⟩ := Option.map_eq_some_iff.mp h2
      exact Q_or hb (ihl a ha) (ihr b hb')
    · simp at h
  | and l r ihl ihr =>
    intro e' h
    simp only [tr] at h
    split at h
    · rename_i t m s hg hr
      cases h
      exact shape_Q hg (ihr _ hr)
    · split at h
      · rename_i hb
        obtain ⟨a, ha, h2⟩ := Option.bind_eq_some_iff.mp h
        obtain ⟨b, hb', rfl⟩ := Option.map_eq_some_iff.mp h2
        exact Q_and hb (ihl a ha) (ihr b hb')
      · simp at h
  | _ => intro e' h; simp [tr] at h

/-- Translating GHA to GitLab keeps the truthiness of every translatable expression, on every environment. -/
theorem tr_sound {e : E} (env : Env) (h : Translatable e) :
    ∃ e', tr e = some e' ∧
      holds .gitlab (mapEnv env) e' = holds .gha (ghaView env) e := by
  have hs := tr_isSome e
  unfold Translatable at h
  rw [h] at hs
  obtain ⟨e', he'⟩ := Option.isSome_iff_exists.mp hs
  exact ⟨e', he', (tr_sound_aux env e e' he').1⟩

/-! ### GitLab to GHA -/

def okFor (m : Mode) (s : String) : Bool :=
  match m with
  | .exact => okEq s
  | _ => okLit s

/-- The GHA test `ref_name <m> s`: == for exact, startsWith, endsWith or contains. -/
def predE (m : Mode) (x : E) (s : String) : E :=
  match m with
  | .exact => .eq x (.str s)
  | .prefix => .startsWith x (.str s)
  | .suffix => .endsWith x (.str s)
  | .sub => .contains x (.str s)

def negE (neg : Bool) (p : E) : E := if neg then .not p else p

/-- `ref_type == 'branch' && p`, the GHA spelling of "this is a branch pipeline and p holds". -/
def guardE (t : RT) (p : E) : E := .and (.eq (.ref .refType) (.str (rtName t))) p

def trRx (neg : Bool) (r : Ref) (m : Mode) (s : String) : Option E :=
  match r with
  | .event =>
    if m = .exact then (evOfGl (lower s)).map fun e => negE neg (predE .exact (.ref .event) (evGha e))
    else none
  | .refName | .sha => if okFor m s then some (negE neg (predE m (.ref r) s)) else none
  | .branch => if okFor m s then some (negE neg (guardE .branch (predE m (.ref .refName) s))) else none
  | .tag => if okFor m s then some (negE neg (guardE .tag (predE m (.ref .refName) s))) else none
  | _ => none

/-- Mirrors expr.Translate (expr/translate.go) from gitlab to gha. -/
def trR : E → Option E
  | .ref .refName => some (.ref .refName)
  | .ref .sha => some (.ref .sha)
  | .ref .event => some (.ref .event)
  | .ref .branch => some (guardE .branch (.ref .refName))
  | .ref .tag => some (guardE .tag (.ref .refName))
  | .rx (.ref r) m s => trRx false r m s
  | .nrx (.ref r) m s => trRx true r m s
  | .not x => (trR x).map .not
  | .and l r => if bv l then (trR l).bind fun a => (trR r).map fun b => .and a b else none
  | .or l r => if bv l then (trR l).bind fun a => (trR r).map fun b => .or a b else none
  | _ => none

def rxOk (r : Ref) (m : Mode) (s : String) : Bool :=
  match r with
  | .event => m == .exact && (evOfGl (lower s)).isSome
  | .refName | .sha | .branch | .tag => okFor m s
  | _ => false

/-- The GitLab expressions that translate to GHA (Mirrors expr.Translatable, expr/translate.go). -/
def trROk : E → Bool
  | .ref r => r == .refName || r == .sha || r == .event || r == .branch || r == .tag
  | .rx (.ref r) m s | .nrx (.ref r) m s => rxOk r m s
  | .not x => trROk x
  | .and l r | .or l r => bv l && trROk l && trROk r
  | _ => false

def TranslatableR (e : E) : Prop := trROk e = true

theorem trRx_isSome (neg : Bool) (r : Ref) (m : Mode) (s : String) :
    (trRx neg r m s).isSome = rxOk r m s := by
  cases r <;> simp [trRx, rxOk]
  case event => by_cases h : m = .exact <;> simp [h]
  all_goals split <;> simp_all

theorem trR_isSome (e : E) : (trR e).isSome = trROk e := by
  induction e with
  | ref r => cases r <;> rfl
  | not x ih => simp [trR, trROk, ← ih]
  | and l r ihl ihr =>
    simp only [trR, trROk]
    by_cases hb : bv l = true <;> simp [hb, ← ihl, ← ihr, Option.isSome_bind, Option.isSome_map, opt_any]
  | or l r ihl ihr =>
    simp only [trR, trROk]
    by_cases hb : bv l = true <;> simp [hb, ← ihl, ← ihr, Option.isSome_bind, Option.isSome_map, opt_any]
  | rx x m s => cases x <;> simp [trR, trROk, trRx_isSome]
  | nrx x m s => cases x <;> simp [trR, trROk, trRx_isSome]
  | _ => simp [trR, trROk]

/-- Translation fails exactly outside the translatable subset (GitLab to GHA). -/
theorem tr_none_iff_rev (e : E) : trR e = none ↔ ¬ TranslatableR e := by
  have h := trR_isSome e
  unfold TranslatableR
  cases hx : trR e <;> simp_all

theorem evOfGl_name {l : String} {e : Ev} (h : evOfGl l = some e) : l = evGl e := by
  unfold evOfGl at h
  split_ifs at h <;> simp at h <;> subst h <;> simp_all [evGl]

theorem ev_agree2 (e e0 : Ev) :
    (lower (evGl e) == evGl e0) = (lower (evGha e) == lower (evGha e0)) := by
  cases e <;> cases e0 <;> decide

theorem okLit_ne {s : String} (h : okLit s = true) : (s == "") = false := by
  simp [okLit] at h
  simpa using h.1

theorem okFor_ne {m : Mode} {s : String} (h : okFor m s = true) : (s == "") = false := by
  cases m <;> simp only [okFor, okEq, Bool.and_eq_true] at h
  · exact okLit_ne h.1
  all_goals exact okLit_ne h

theorem pred_eval {m : Mode} {s : String} (hok : okFor m s = true) (w : View) (r : Ref) :
    eval .gha w (predE m (.ref r) s) = .bool (matchV m s (refV w r)) := by
  have hs := okFor_ne hok
  cases m
  · simp [predE, eval, match_exact_eq hs w r]
  all_goals cases h : w r <;> simp [predE, eval, refV, affixV, matchV, h]

theorem eval_negE (neg : Bool) (v : View) (p : E) (b : Bool) (h : eval .gha v p = .bool b) :
    eval .gha v (negE neg p) = .bool (negIf neg b) := by
  cases neg <;> simp [negE, negIf, eval, h, truthy]

theorem rt_guard (env : Env) (t : RT) :
    eval .gha (ghaView env) (.eq (.ref .refType) (.str (rtName t))) = .bool (decide (env.refType = some t)) := by
  have hg : typeGuard (.eq (.ref .refType) (.str (rtName t))) = some t := by
    cases t <;> decide
  exact typeGuard_eval env hg

theorem eval_and (sem : Sem) (v : View) (l r : E) :
    eval sem v (.and l r) = if logicalTruthy sem (eval sem v l) then eval sem v r else eval sem v l := by
  simp [eval]

theorem guard_eval (env : Env) (t : RT) (p : E) (b : Bool) (hp : eval .gha (ghaView env) p = .bool b) :
    eval .gha (ghaView env) (guardE t p) = .bool (decide (env.refType = some t) && b) := by
  unfold guardE
  rw [eval_and, rt_guard env t, hp]
  by_cases h : env.refType = some t <;> simp [h, logicalTruthy, truthy]

theorem refV_rt (env : Env) (t : RT) :
    refV (mapEnv env) (rtRef t) = if env.refType = some t then refV (mapEnv env) .refName else .null := by
  by_cases h : env.refType = some t <;> cases t <;> simp [rtRef, mapEnv, refV, h]

theorem guard_match (env : Env) (t : RT) (m : Mode) (s : String) (hok : okFor m s = true) :
    eval .gha (ghaView env) (guardE t (predE m (.ref .refName) s)) =
      .bool (matchV m s (refV (mapEnv env) (rtRef t))) := by
  rw [guard_eval env t _ _ (pred_eval hok _ _), refV_rt]
  by_cases h : env.refType = some t
  · simp [h, refV, mapEnv, ghaView]
  · simp [h, matchV]

theorem trRx_eval {neg : Bool} {r : Ref} {m : Mode} {s : String} {e' : E} (env : Env)
    (h : trRx neg r m s = some e') :
    eval .gha (ghaView env) e' = .bool (negIf neg (matchV m s (refV (mapEnv env) r))) := by
  cases r <;> simp only [trRx] at h
  case event =>
    split at h
    · rename_i hm
      subst hm
      obtain ⟨e0, he0, rfl⟩ := Option.map_eq_some_iff.mp h
      have hl := evOfGl_name he0
      have hs : (evGha e0 == "") = false := by cases e0 <;> decide
      refine eval_negE neg _ _ _ ?_
      cases ho : env.event with
      | none => simp [predE, eval, refV, mapEnv, ghaView, ho, matchV, eqV, hs]
      | some e =>
        simp only [predE, eval, refV, mapEnv, ghaView, ho, Option.map_some, matchV, modeTest, eqV, evGha_lower]
        rw [hl, ev_agree2 e e0]
        simp [evGha_lower]
    · simp at h
  case refName | sha =>
    split at h
    · rename_i hk
      cases h
      refine eval_negE neg _ _ _ ?_
      first
        | (rw [pred_eval hk]; simp [mapEnv, ghaView, refV]; done)
        | skip
    · simp at h
  case branch | tag =>
    split at h
    · rename_i hk
      cases h
      refine eval_negE neg _ _ _ ?_
      first
        | exact guard_match env .branch m s hk
        | exact guard_match env .tag m s hk
    · simp at h
  all_goals simp at h

/-- Reverse soundness invariant: equal truthiness, and both sides boolean-valued on boolean-valued sources. -/
def QR (env : Env) (e e' : E) : Prop :=
  holds .gha (ghaView env) e' = holds .gitlab (mapEnv env) e ∧
  (bv e = true → eval .gitlab (mapEnv env) e = .bool (holds .gitlab (mapEnv env) e) ∧
    eval .gha (ghaView env) e' = .bool (holds .gitlab (mapEnv env) e))

theorem QR_of_bool {env : Env} {e e' : E} {b : Bool}
    (h1 : eval .gitlab (mapEnv env) e = .bool b) (h2 : eval .gha (ghaView env) e' = .bool b) :
    QR env e e' := by
  simp [QR, holds, h1, h2, truthy]

theorem QR_not {env : Env} {x a : E} (h : QR env x a) : QR env (.not x) (.not a) := by
  have h1 : eval .gitlab (mapEnv env) (.not x) = .bool (!holds .gitlab (mapEnv env) x) := rfl
  have h2 : eval .gha (ghaView env) (.not a) = .bool (!holds .gha (ghaView env) a) := rfl
  exact QR_of_bool (b := !holds .gitlab (mapEnv env) x) h1 (by rw [h2, h.1])

theorem QR_or {env : Env} {l r a b : E} (hbl : bv l = true) (hl : QR env l a) (hr : QR env r b) :
    QR env (.or l r) (.or a b) := by
  obtain ⟨e1, e1'⟩ := hl.2 hbl
  by_cases h : holds .gitlab (mapEnv env) l = true
  · have g : eval .gitlab (mapEnv env) (.or l r) = .bool true := by
      simp [eval, logicalTruthy, rubyTruthy, e1, h]
    have g2 : eval .gha (ghaView env) (.or a b) = .bool true := by
      simp [eval, logicalTruthy, truthy, e1', h]
    exact QR_of_bool g g2
  · have h' : holds .gitlab (mapEnv env) l = false := by simpa using h
    have g : eval .gitlab (mapEnv env) (.or l r) = eval .gitlab (mapEnv env) r := by
      simp [eval, logicalTruthy, rubyTruthy, e1, h']
    have g2 : eval .gha (ghaView env) (.or a b) = eval .gha (ghaView env) b := by
      simp [eval, logicalTruthy, truthy, e1', h']
    refine ⟨?_, fun hb => ?_⟩
    · simp only [holds, g, g2]; exact hr.1
    · have hbr : bv r = true := by simpa [bv, hbl] using hb
      obtain ⟨r1, r2⟩ := hr.2 hbr
      rw [g, g2]; simpa [holds, g] using ⟨r1, r2⟩

theorem QR_and {env : Env} {l r a b : E} (hbl : bv l = true) (hl : QR env l a) (hr : QR env r b) :
    QR env (.and l r) (.and a b) := by
  obtain ⟨e1, e1'⟩ := hl.2 hbl
  by_cases h : holds .gitlab (mapEnv env) l = true
  · have g : eval .gitlab (mapEnv env) (.and l r) = eval .gitlab (mapEnv env) r := by
      simp [eval, logicalTruthy, rubyTruthy, e1, h]
    have g2 : eval .gha (ghaView env) (.and a b) = eval .gha (ghaView env) b := by
      simp [eval, logicalTruthy, truthy, e1', h]
    refine ⟨?_, fun hb => ?_⟩
    · simp only [holds, g, g2]; exact hr.1
    · have hbr : bv r = true := by simpa [bv, hbl] using hb
      obtain ⟨r1, r2⟩ := hr.2 hbr
      rw [g, g2]; simpa [holds, g] using ⟨r1, r2⟩
  · have h' : holds .gitlab (mapEnv env) l = false := by simpa using h
    have g : eval .gitlab (mapEnv env) (.and l r) = .bool false := by
      simp [eval, logicalTruthy, rubyTruthy, e1, h']
    have g2 : eval .gha (ghaView env) (.and a b) = .bool false := by
      simp [eval, logicalTruthy, truthy, e1', h']
    exact QR_of_bool g g2

theorem holds_guard_ref (env : Env) (t : RT) :
    holds .gha (ghaView env) (guardE t (.ref .refName)) = holds .gitlab (mapEnv env) (.ref (rtRef t)) := by
  unfold holds
  rw [guardE, eval_and, rt_guard env t]
  by_cases h : env.refType = some t <;> cases t <;>
    simp [h, eval, logicalTruthy, truthy, refV, mapEnv, ghaView, rtRef]

theorem tr_sound_rev_aux (env : Env) : ∀ e e', trR e = some e' → QR env e e' := by
  intro e
  induction e with
  | ref r =>
    intro e' h
    cases r <;> simp only [trR, Option.some.injEq] at h <;> try (simp at h; done)
    case branch => subst h; exact ⟨holds_guard_ref env .branch, by simp [bv]⟩
    case tag => subst h; exact ⟨holds_guard_ref env .tag, by simp [bv]⟩
    all_goals subst h
    all_goals refine ⟨?_, by simp [bv]⟩
    all_goals first
      | (simp [holds, eval, refV, mapEnv, ghaView]; done)
      | (cases ho : env.event <;> simp [holds, eval, refV, mapEnv, ghaView, ho, truthy]
         rename_i e0; cases e0 <;> simp [evGl, evGha])
  | rx x m s =>
    intro e' h
    cases x <;> simp only [trR] at h <;> try (simp at h; done)
    rename_i r0 _
    have := trRx_eval env h
    exact QR_of_bool (b := matchV m s (refV (mapEnv env) r0)) (by simp [eval]) (by simpa [negIf] using this)
  | nrx x m s =>
    intro e' h
    cases x <;> simp only [trR] at h <;> try (simp at h; done)
    rename_i r0 _
    have := trRx_eval env h
    exact QR_of_bool (b := !matchV m s (refV (mapEnv env) r0)) (by simp [eval]) (by simpa [negIf] using this)
  | not x ih =>
    intro e' h
    simp only [trR] at h
    obtain ⟨a, ha, rfl⟩ := Option.map_eq_some_iff.mp h
    exact QR_not (ih a ha)
  | or l r ihl ihr =>
    intro e' h
    simp only [trR] at h
    split at h
    · rename_i hb
      obtain ⟨a, ha, h2⟩ := Option.bind_eq_some_iff.mp h
      obtain ⟨b, hb', rfl⟩ := Option.map_eq_some_iff.mp h2
      exact QR_or hb (ihl a ha) (ihr b hb')
    · simp at h
  | and l r ihl ihr =>
    intro e' h
    simp only [trR] at h
    split at h
    · rename_i hb
      obtain ⟨a, ha, h2⟩ := Option.bind_eq_some_iff.mp h
      obtain ⟨b, hb', rfl⟩ := Option.map_eq_some_iff.mp h2
      exact QR_and hb (ihl a ha) (ihr b hb')
    · simp at h
  | _ => intro e' h; simp [trR] at h

/-- Translating GitLab to GHA keeps the truthiness of every translatable expression, on every environment. -/
theorem tr_sound_rev {e : E} (env : Env) (h : TranslatableR e) :
    ∃ e', trR e = some e' ∧
      holds .gha (ghaView env) e' = holds .gitlab (mapEnv env) e := by
  have hs := trR_isSome e
  unfold TranslatableR at h
  rw [h] at hs
  obtain ⟨e', he'⟩ := Option.isSome_iff_exists.mp hs
  exact ⟨e', he', (tr_sound_rev_aux env e e' he').1⟩

/-- Why the case rule exists: copying GHA's `ref_name == 'main'` to GitLab's `==` changes the result,
because GHA ignores case and GitLab does not. -/
theorem naive_eq_unsound : ∃ env : Env,
    holds .gitlab (mapEnv env) (.eq (.ref .refName) (.str "main")) ≠
      holds .gha (ghaView env) (.eq (.ref .refName) (.str "main")) :=
  ⟨⟨some "MAIN", none, none, none, none⟩, by decide⟩

/-- Why left operands must be boolean-valued: an empty ref_name makes GHA's `ref_name && x` false but
GitLab's operand test treats "" as truthy, so the bare conjunction diverges. -/
theorem naive_and_unsound : ∃ env : Env,
    holds .gitlab (mapEnv env) (.and (.ref .refName) (.ref .sha)) ≠
      holds .gha (ghaView env) (.and (.ref .refName) (.ref .sha)) :=
  ⟨⟨some "", none, none, some "abc", none⟩, by decide⟩

end Translate
end Pipemaster
