import Mathlib.Data.List.Sort
import Mathlib.Data.String.Basic
import Pipemaster.IR

namespace Pipemaster
namespace Ledger

/-- The accounting entry of one feature use (report.Entry). -/
structure Entry where
  use : FeatureUse
  level : Level
  strategy : String
  detail : String
  deriving DecidableEq, Repr

/-- An emitter call on the ledger: Decide or Note. -/
inductive Op
  | decide (u : FeatureUse)
  | note (u : FeatureUse) (s : Support) (detail : String)
  deriving DecidableEq, Repr

/-- The feature use an operation accounts. -/
def Op.use : Op → FeatureUse
  | .decide u => u
  | .note u _ _ => u

/-- Final view: uses emitted natively, and the report entries. -/
structure LedgerState where
  emitted : List FeatureUse
  report : List Entry
  deriving DecidableEq, Repr

def entryFor (u : FeatureUse) (s : Support) (detail : String) : Entry :=
  { use := u, level := s.level, strategy := s.strategy, detail := detail }

/-- Sets the entry of its use, appending when the use has none. -/
def upsert (st : List Entry) (e : Entry) : List Entry :=
  if st.any (fun x => x.use == e.use) then st.map (fun x => if x.use == e.use then e else x)
  else st ++ [e]

def find (st : List Entry) (u : FeatureUse) : Option Entry :=
  st.find? (fun x => x.use == u)

/-- The current entry of a use; the matrix entry when none exists yet. -/
def curEntry (caps : CapsMatrix) (st : List Entry) (u : FeatureUse) : Entry :=
  match find st u with
  | some c => c
  | none =>
    let base := caps.lookup u.feature
    entryFor u base base.caveat

/-- One Decide or Note call. -/
def step (caps : CapsMatrix) (st : List Entry) : Op → List Entry
  | .decide u =>
    let s := caps.lookup u.feature
    match find st u with
    | none => upsert st (entryFor u s s.caveat)
    | some cur =>
      if cur.level.toNat < s.level.toNat then upsert st (entryFor u s s.caveat) else st
  | .note u s d =>
    let cur := curEntry caps st u
    upsert st (if cur.level.toNat ≤ s.level.toNat then entryFor u s d else cur)

def useLe (a b : FeatureUse) : Bool :=
  decide (a.irPath < b.irPath) || (a.irPath == b.irPath && !decide (b.feature < a.feature))

/-- Sorts by (irPath, feature). -/
def sortBy {α : Type} (key : α → FeatureUse) (l : List α) : List α :=
  l.mergeSort fun a b => useLe (key a) (key b)

/-- The state after the operations, in emission order. -/
def final (caps : CapsMatrix) (ops : List Op) : List Entry :=
  ops.foldl (step caps) []

/-- Mirrors emitkit.Ledger (provider/emitkit/ledger.go). -/
def run (caps : CapsMatrix) (ops : List Op) : LedgerState :=
  let st := final caps ops
  { emitted := sortBy id ((st.filter fun e => e.level == .native).map Entry.use)
    report := sortBy Entry.use (st.filter fun e => e.level != .native) }

/-- Uses that Finish reports as unaccounted. -/
def unaccounted (caps : CapsMatrix) (ops : List Op) (uses : List FeatureUse) : List FeatureUse :=
  sortBy id (uses.filter fun u => (find (final caps ops) u).isNone)

/-- Levels of the Note calls on a use. -/
def noteLevels (ops : List Op) (u : FeatureUse) : List Nat :=
  ops.filterMap fun
    | .note v s _ => if v = u then some s.level.toNat else none
    | .decide _ => none

/-- Matrix level, raised by every Note level on the use. -/
def effLevel (caps : CapsMatrix) (ops : List Op) (u : FeatureUse) : Nat :=
  (noteLevels ops u).foldl Nat.max (caps.lookup u.feature).level.toNat

theorem le_effLevel (caps : CapsMatrix) (ops : List Op) (u : FeatureUse) :
    (caps.lookup u.feature).level.toNat ≤ effLevel caps ops u := by
  unfold effLevel
  generalize (caps.lookup u.feature).level.toNat = a
  induction (noteLevels ops u) generalizing a with
  | nil => exact le_refl _
  | cons x t ih => exact le_trans (Nat.le_max_left a x) (ih _)

theorem noteLevels_eq_nil {ops : List Op} {u : FeatureUse} (h : ∀ op ∈ ops, op.use ≠ u) :
    noteLevels ops u = [] := by
  induction ops with
  | nil => rfl
  | cons op t ih =>
    have ht : ∀ op' ∈ t, op'.use ≠ u := fun o ho => h o (List.mem_cons_of_mem _ ho)
    have h0 := h op (List.mem_cons_self ..)
    cases op with
    | decide v => simpa [noteLevels] using ih ht |> fun h' => by simpa [noteLevels] using h'
    | note v s d =>
      have : v ≠ u := h0
      simp [noteLevels, this] at ih ⊢
      exact ih ht

theorem effLevel_decide (caps : CapsMatrix) (past : List Op) (u w : FeatureUse) :
    effLevel caps (past ++ [.decide u]) w = effLevel caps past w := by
  simp [effLevel, noteLevels, List.filterMap_append]

theorem effLevel_note_ne (caps : CapsMatrix) (past : List Op) (u w : FeatureUse) (s d)
    (h : u ≠ w) : effLevel caps (past ++ [.note u s d]) w = effLevel caps past w := by
  simp [effLevel, noteLevels, List.filterMap_append, h]

theorem effLevel_note (caps : CapsMatrix) (past : List Op) (u : FeatureUse) (s d) :
    effLevel caps (past ++ [.note u s d]) u = max (effLevel caps past u) s.level.toNat := by
  simp [effLevel, noteLevels, List.filterMap_append, List.foldl_append]

/-- What every reachable state satisfies after the operations `past`. -/
structure Inv (caps : CapsMatrix) (st : List Entry) (past : List Op) : Prop where
  uniq : ∀ e1 ∈ st, ∀ e2 ∈ st, e1.use = e2.use → e1 = e2
  exists_iff : ∀ u, (∃ e ∈ st, e.use = u) ↔ ∃ op ∈ past, op.use = u
  level : ∀ e ∈ st, e.level.toNat = effLevel caps past e.use

theorem mem_upsert {st : List Entry} {e e' : Entry} :
    e' ∈ upsert st e ↔ e' = e ∨ (e' ∈ st ∧ e'.use ≠ e.use) := by
  unfold upsert
  by_cases h : st.any (fun x => x.use == e.use) = true
  · simp only [h, ↓reduceIte, List.mem_map]
    obtain ⟨x, hx, hxe⟩ := List.any_eq_true.1 h
    have hxe' : x.use = e.use := by simpa using hxe
    constructor
    · rintro ⟨y, hy, hye⟩
      by_cases hu : y.use = e.use
      · left; simp [hu] at hye; exact hye.symm
      · right; simp [hu] at hye; subst hye; exact ⟨hy, hu⟩
    · rintro (rfl | ⟨hm, hu⟩)
      · exact ⟨x, hx, by simp [hxe']⟩
      · exact ⟨e', hm, by simp [hu]⟩
  · simp only [h, Bool.false_eq_true, ↓reduceIte, List.mem_append, List.mem_singleton]
    have h' : ∀ x ∈ st, x.use ≠ e.use := by
      intro x hx hxe
      exact h (List.any_eq_true.2 ⟨x, hx, by simpa using hxe⟩)
    constructor
    · rintro (hm | rfl)
      · exact Or.inr ⟨hm, h' _ hm⟩
      · exact Or.inl rfl
    · rintro (rfl | ⟨hm, _⟩)
      · exact Or.inr rfl
      · exact Or.inl hm

theorem inv_upsert {caps : CapsMatrix} {st : List Entry} {past : List Op} (hi : Inv caps st past)
    (op : Op) (e : Entry) (he : e.use = op.use)
    (hl : e.level.toNat = effLevel caps (past ++ [op]) e.use)
    (hne : ∀ w, w ≠ op.use → effLevel caps (past ++ [op]) w = effLevel caps past w) :
    Inv caps (upsert st e) (past ++ [op]) := by
  constructor
  · intro e1 h1 e2 h2 hu
    rcases mem_upsert.1 h1 with rfl | ⟨h1, n1⟩ <;> rcases mem_upsert.1 h2 with rfl | ⟨h2, n2⟩
    · rfl
    · exact absurd hu.symm n2
    · exact absurd hu n1
    · exact hi.uniq _ h1 _ h2 hu
  · intro w
    constructor
    · rintro ⟨e', hm, rfl⟩
      rcases mem_upsert.1 hm with rfl | ⟨hm, _⟩
      · exact ⟨op, by simp, he.symm⟩
      · obtain ⟨o, ho, hou⟩ := (hi.exists_iff _).1 ⟨e', hm, rfl⟩
        exact ⟨o, by simp [ho], hou⟩
    · rintro ⟨o, ho, rfl⟩
      rcases List.mem_append.1 ho with ho | ho
      · by_cases hw : o.use = e.use
        · exact ⟨e, mem_upsert.2 (Or.inl rfl), hw.symm⟩
        · obtain ⟨e', hm, hu⟩ := (hi.exists_iff _).2 ⟨o, ho, rfl⟩
          exact ⟨e', mem_upsert.2 (Or.inr ⟨hm, by rw [hu]; exact hw⟩), hu⟩
      · have : o = op := by simpa using ho
        subst this
        exact ⟨e, mem_upsert.2 (Or.inl rfl), he⟩
  · intro e' hm
    rcases mem_upsert.1 hm with rfl | ⟨hm, hu⟩
    · exact hl
    · rw [hne _ (by rwa [← he]), hi.level _ hm]

theorem step_inv {caps : CapsMatrix} {st : List Entry} {past : List Op} (hi : Inv caps st past)
    (op : Op) : Inv caps (step caps st op) (past ++ [op]) := by
  cases op with
  | decide u =>
    have hne : ∀ w, w ≠ (Op.decide u).use → effLevel caps (past ++ [.decide u]) w = effLevel caps past w :=
      fun w _ => effLevel_decide caps past u w
    cases hf : find st u with
    | none =>
      have hnone : ∀ e ∈ st, e.use ≠ u := by
        intro e he hu
        have := List.find?_eq_none.1 hf e he
        simp [hu] at this
      have hnop : ∀ o ∈ past, o.use ≠ u := by
        intro o ho hou
        obtain ⟨e, he, hu⟩ := (hi.exists_iff u).2 ⟨o, ho, hou⟩
        exact hnone e he hu
      simp only [step, hf]
      apply inv_upsert hi (.decide u) _ rfl
      · simp only [entryFor, effLevel_decide]
        simp [effLevel, noteLevels_eq_nil hnop]
      · exact hne
    | some cur =>
      have hcm : cur ∈ st := List.mem_of_find?_eq_some hf
      have hcu : cur.use = u := by simpa using List.find?_some hf
      have hlv := hi.level cur hcm
      have hge := le_effLevel caps past cur.use
      rw [hcu] at hge hlv
      simp only [step, hf]
      simp only [show ¬ (cur.level.toNat < (caps.lookup u.feature).level.toNat) from by omega,
        ↓reduceIte]
      refine ⟨hi.uniq, fun w => ?_, fun e he => ?_⟩
      · rw [hi.exists_iff w]
        constructor
        · rintro ⟨o, ho, h⟩; exact ⟨o, by simp [ho], h⟩
        · rintro ⟨o, ho, h⟩
          rcases List.mem_append.1 ho with ho | ho
          · exact ⟨o, ho, h⟩
          · have : o = .decide u := by simpa using ho
            subst this
            simp only [Op.use] at h
            subst h
            obtain ⟨o', ho', hu'⟩ := (hi.exists_iff u).1 ⟨cur, hcm, hcu⟩
            exact ⟨o', ho', hu'⟩
      · rw [hi.level e he, effLevel_decide]
  | note u s d =>
    have hne : ∀ w, w ≠ (Op.note u s d).use →
        effLevel caps (past ++ [.note u s d]) w = effLevel caps past w :=
      fun w hw => effLevel_note_ne caps past u w s d (fun h => hw h.symm)
    have hcur : (curEntry caps st u).use = u ∧
        (curEntry caps st u).level.toNat = effLevel caps past u := by
      unfold curEntry
      cases hf : find st u with
      | some c =>
        have hcu : c.use = u := by simpa using List.find?_some hf
        exact ⟨hcu, by rw [hi.level c (List.mem_of_find?_eq_some hf), hcu]⟩
      | none =>
        have hnone : ∀ e ∈ st, e.use ≠ u := by
          intro e he hu
          have := List.find?_eq_none.1 hf e he
          simp [hu] at this
        have hnop : ∀ o ∈ past, o.use ≠ u := by
          intro o ho hou
          obtain ⟨e, he, hu⟩ := (hi.exists_iff u).2 ⟨o, ho, hou⟩
          exact hnone e he hu
        exact ⟨rfl, by simp [entryFor, effLevel, noteLevels_eq_nil hnop]⟩
    obtain ⟨hcu, hcl⟩ := hcur
    unfold step
    simp only
    apply inv_upsert hi (.note u s d) _ _ _ hne
    · split <;> simp [entryFor, Op.use, hcu]
    · split
      · rename_i hle
        simp only [entryFor, effLevel_note]
        rw [hcl] at hle
        omega
      · rename_i hle
        rw [hcu, effLevel_note, hcl]
        omega

theorem fold_inv (caps : CapsMatrix) (ops : List Op) :
    ∀ (st : List Entry) (past : List Op), Inv caps st past →
      Inv caps (ops.foldl (step caps) st) (past ++ ops) := by
  induction ops with
  | nil => intro st past h; simpa using h
  | cons op t ih =>
    intro st past h
    have := ih _ _ (step_inv h op)
    simpa [List.append_assoc] using this

theorem final_inv (caps : CapsMatrix) (ops : List Op) : Inv caps (final caps ops) ops := by
  have := fold_inv caps ops [] [] ⟨by simp, by simp, by simp⟩
  simpa [final] using this

theorem mem_emitted {caps : CapsMatrix} {ops : List Op} {u : FeatureUse} :
    u ∈ (run caps ops).emitted ↔ ∃ e ∈ final caps ops, e.use = u ∧ e.level = .native := by
  simp [run, sortBy, List.mem_mergeSort, and_assoc, and_comm]

theorem mem_report {caps : CapsMatrix} {ops : List Op} {e : Entry} :
    e ∈ (run caps ops).report ↔ e ∈ final caps ops ∧ e.level ≠ .native := by
  simp [run, sortBy, List.mem_mergeSort]

/-- Proved about the model: every operated use is emitted or reported. -/
theorem complete (caps : CapsMatrix) (ops : List Op) :
    ∀ u, (∃ op ∈ ops, op.use = u) →
      u ∈ (run caps ops).emitted ∨ ∃ e ∈ (run caps ops).report, e.use = u := by
  intro u h
  obtain ⟨e, he, hu⟩ := ((final_inv caps ops).exists_iff u).2 h
  by_cases hl : e.level = .native
  · exact Or.inl (mem_emitted.2 ⟨e, he, hu, hl⟩)
  · exact Or.inr ⟨e, mem_report.2 ⟨he, hl⟩, hu⟩

/-- No use is both emitted and reported. -/
theorem emitted_report_disjoint (caps : CapsMatrix) (ops : List Op) :
    ∀ u ∈ (run caps ops).emitted, ∀ e ∈ (run caps ops).report, e.use ≠ u := by
  intro u hu e he heu
  obtain ⟨e', he', hu', hl'⟩ := mem_emitted.1 hu
  obtain ⟨hem, hne⟩ := mem_report.1 he
  have := (final_inv caps ops).uniq e hem e' he' (by rw [heu, hu'])
  exact hne (by rw [this]; exact hl')

/-- Each use has at most one report entry. -/
theorem report_unique (caps : CapsMatrix) (ops : List Op) :
    ∀ e1 ∈ (run caps ops).report, ∀ e2 ∈ (run caps ops).report, e1.use = e2.use → e1 = e2 := by
  intro e1 h1 e2 h2 hu
  exact (final_inv caps ops).uniq e1 (mem_report.1 h1).1 e2 (mem_report.1 h2).1 hu

/-- A reported level is the matrix level raised by every Note level on the use. -/
theorem report_level (caps : CapsMatrix) (ops : List Op) :
    ∀ e ∈ (run caps ops).report,
      e.level.toNat = (noteLevels ops e.use).foldl Nat.max (caps.lookup e.use.feature).level.toNat :=
  fun e he => (final_inv caps ops).level e (mem_report.1 he).1

/-- Finish reports exactly the uses that no operation accounted. -/
theorem finish_unaccounted (caps : CapsMatrix) (ops : List Op) (uses : List FeatureUse) :
    ∀ u, u ∈ unaccounted caps ops uses ↔ u ∈ uses ∧ ∀ op ∈ ops, op.use ≠ u := by
  intro u
  have key : (find (final caps ops) u).isNone = true ↔ ∀ op ∈ ops, op.use ≠ u := by
    constructor
    · intro h op hop hou
      obtain ⟨e, he, hu⟩ := ((final_inv caps ops).exists_iff u).2 ⟨op, hop, hou⟩
      have := List.find?_eq_none.1 (Option.isNone_iff_eq_none.1 h) e he
      simp [hu] at this
    · intro h
      apply Option.isNone_iff_eq_none.2
      apply List.find?_eq_none.2
      intro e he hb
      have hu : e.use = u := by simpa using hb
      obtain ⟨op, hop, hou⟩ := ((final_inv caps ops).exists_iff u).1 ⟨e, he, hu⟩
      exact h op hop hou
  simp [unaccounted, sortBy, List.mem_mergeSort, key]

end Ledger
end Pipemaster
