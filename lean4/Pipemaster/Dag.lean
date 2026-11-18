import Mathlib.Logic.Relation
import Mathlib.Order.ConditionallyCompleteLattice.Basic
import Mathlib.Order.Lattice.Nat
import Mathlib.Data.Fintype.Card
import Mathlib.Data.List.Sort
import Pipemaster.IR

namespace Pipemaster
namespace Dag

/-- Why Layers fails; an unknown need is reported before a cycle. -/
inductive Err
  | unknownNeed
  | cycle
  deriving DecidableEq, Repr

/-- The needs of the first job with this id; later duplicates are shadowed. -/
def needsOf (js : List Job) (x : String) : List Need :=
  match js.find? (fun k => k.id == x) with
  | some j => j.needs
  | none => []

/-- Distinct job ids, sorted. -/
def ids (js : List Job) : List String :=
  (js.map Job.id).eraseDups.mergeSort (fun a b => !decide (b < a))

/-- Edges that exist: a need naming a present job. -/
def deps (js : List Job) (x : String) : List String :=
  ((needsOf js x).map Need.jobId).filter (fun y => (ids js).contains y)

/-- A required need on a missing job. -/
def hasDangling (js : List Job) : Bool :=
  (ids js).any fun x =>
    (needsOf js x).any fun n => !n.optional && !(ids js).contains n.jobId

/-- A job not yet placed whose present needs are all placed. -/
def ready (js : List Job) (placed : List String) (x : String) : Bool :=
  !placed.contains x && (deps js x).all placed.contains

/-- Peels ready jobs layer by layer; each round is one layer. -/
def peel (js : List Job) (L : List String) : Nat → List String → List (List String)
  | 0, _ => []
  | f + 1, placed =>
    let new := L.filter (ready js placed)
    if new.isEmpty then [] else new :: peel js L f (placed ++ new)

/-- The layering, or why it failed. Fuel is the number of jobs. -/
def layersE (js : List Job) : Except Err (List (List String)) :=
  if hasDangling js then .error .unknownNeed
  else
    let L := ids js
    let ls := peel js L L.length []
    if L.all (fun x => ls.flatten.contains x) then .ok ls else .error .cycle

/-- Mirrors ir.Layers (ir/dag.go). -/
def layers (js : List Job) : Option (List (List String)) :=
  (layersE js).toOption

/-- Index of the first layer holding the id; the layer count when absent. -/
def layerOf (ls : List (List String)) (x : String) : Nat :=
  ls.findIdx (fun l => l.contains x)

/-- The job that stands for its id: the first one listed. -/
def IsEff (js : List Job) (j : Job) : Prop :=
  js.find? (fun k => k.id == j.id) = some j

/-- x needs y, and both name present jobs. -/
def Edge (js : List Job) (x y : String) : Prop :=
  x ∈ ids js ∧ y ∈ deps js x

/-- Some job is on a needs cycle. -/
def HasCycle (js : List Job) : Prop :=
  ∃ x, Relation.TransGen (Edge js) x x

/-- Some effective job has a required need naming no job. -/
def UnknownNeed (js : List Job) : Prop :=
  ∃ j, IsEff js j ∧ ∃ n ∈ j.needs, n.optional = false ∧ n.jobId ∉ ids js

/-- A needs chain of n edges starting at x. -/
inductive Chain (js : List Job) : String → Nat → Prop
  | nil {x} : x ∈ ids js → Chain js x 0
  | cons {x y n} : Edge js x y → Chain js y n → Chain js x (n + 1)

/-- Edge count of the longest needs chain. -/
noncomputable def longestPath (js : List Job) : Nat :=
  sSup {n | ∃ x, Chain js x n}


theorem mem_ids {js : List Job} {x : String} : x ∈ ids js ↔ ∃ j ∈ js, j.id = x := by
  simp [ids, List.mem_mergeSort, List.mem_eraseDups]

theorem mem_deps {js : List Job} {x y : String} :
    y ∈ deps js x ↔ (∃ n ∈ needsOf js x, n.jobId = y) ∧ y ∈ ids js := by
  simp [deps, and_comm]

theorem ready_iff {js : List Job} {P : List String} {x : String} :
    ready js P x = true ↔ x ∉ P ∧ ∀ y ∈ deps js x, y ∈ P := by
  simp [ready]

/-- The layer list is exactly what peeling with these rules produces. -/
structure Peeled (js : List Job) (ls : List (List String)) : Prop where
  mem : ∀ k (hk : k < ls.length) x,
    x ∈ ls[k] ↔ x ∈ ids js ∧ x ∉ (ls.take k).flatten ∧ ∀ y ∈ deps js x, y ∈ (ls.take k).flatten
  nonempty : ∀ l ∈ ls, l ≠ []

theorem peel_get (js : List Job) (L : List String) (f : Nat) :
    ∀ (placed : List String) (k : Nat) (hk : k < (peel js L f placed).length),
      (peel js L f placed)[k] =
        L.filter (ready js (placed ++ ((peel js L f placed).take k).flatten)) := by
  induction f with
  | zero => intro placed k hk; simp [peel] at hk
  | succ f ih =>
    intro placed k hk
    by_cases hn : (L.filter (ready js placed)).isEmpty = true
    · simp [peel, hn] at hk
    · have hp : peel js L (f + 1) placed
          = L.filter (ready js placed) :: peel js L f (placed ++ L.filter (ready js placed)) := by
        simp [peel, hn]
      cases k with
      | zero => simp [hp]
      | succ k =>
        have hk' : k < (peel js L f (placed ++ L.filter (ready js placed))).length := by
          rw [hp] at hk; simpa using hk
        simp only [hp, List.getElem_cons_succ, List.take_succ_cons, List.flatten_cons]
        rw [ih _ k hk']
        simp [List.append_assoc]

theorem peel_ne (js : List Job) (L : List String) (f : Nat) :
    ∀ (placed : List String), ∀ l ∈ peel js L f placed, l ≠ [] := by
  induction f with
  | zero => intro placed l hl; simp [peel] at hl
  | succ f ih =>
    intro placed l hl
    by_cases hn : (L.filter (ready js placed)).isEmpty = true
    · simp [peel, hn] at hl
    · simp only [peel, hn, Bool.false_eq_true, ite_false, List.mem_cons] at hl
      rcases hl with rfl | hl
      · simpa using hn
      · exact ih _ l hl

theorem peel_peeled (js : List Job) (f : Nat) : Peeled js (peel js (ids js) f []) := by
  constructor
  · intro k hk x
    rw [peel_get js (ids js) f [] k hk]
    simp [List.mem_filter, ready_iff]
  · exact peel_ne js (ids js) f []

theorem mem_take_flatten {ls : List (List String)} {k : Nat} {x : String} :
    x ∈ (ls.take k).flatten ↔ ∃ i, ∃ _ : i < ls.length, i < k ∧ x ∈ ls[i] := by
  rw [List.mem_flatten]
  constructor
  · rintro ⟨l, hl, hx⟩
    obtain ⟨i, hi, rfl⟩ := List.getElem_of_mem hl
    simp only [List.length_take] at hi
    exact ⟨i, by omega, by omega, by simpa using hx⟩
  · rintro ⟨i, hi, hik, hx⟩
    exact ⟨ls[i], List.mem_iff_getElem.2 ⟨i, by simp; omega, by simp⟩, hx⟩

/-- All ids are placed. -/
def Complete (js : List Job) (ls : List (List String)) : Prop :=
  ∀ x ∈ ids js, x ∈ ls.flatten

section Abstract
variable {js : List Job} {ls : List (List String)}

theorem Peeled.take_mono (_hp : Peeled js ls) {i k : Nat} (h : i ≤ k) {x : String}
    (hx : x ∈ (ls.take i).flatten) : x ∈ (ls.take k).flatten := by
  rw [mem_take_flatten] at hx ⊢
  obtain ⟨a, ha, hai, hxa⟩ := hx
  exact ⟨a, ha, by omega, hxa⟩

theorem Peeled.layerOf_eq (hp : Peeled js ls) {k : Nat} (hk : k < ls.length) {x : String}
    (hx : x ∈ ls[k]) : layerOf ls x = k := by
  have hmem := (hp.mem k hk x).1 hx
  unfold layerOf
  apply le_antisymm
  · by_contra hlt
    have hlt' : k < ls.findIdx (fun l => l.contains x) := by omega
    have := List.not_of_lt_findIdx hlt'
    simp [hx] at this
  · by_contra hlt
    have hlt' : ls.findIdx (fun l => l.contains x) < k := by omega
    have hi : ls.findIdx (fun l => l.contains x) < ls.length := by omega
    have h2 := List.findIdx_getElem (xs := ls) (p := fun l => l.contains x) (w := hi)
    apply hmem.2.1
    rw [mem_take_flatten]
    exact ⟨_, hi, hlt', by simpa using h2⟩

theorem Peeled.exists_layer (_hp : Peeled js ls) (hc : Complete js ls) {x : String}
    (hx : x ∈ ids js) : ∃ k, ∃ hk : k < ls.length, x ∈ ls[k] := by
  have := hc x hx
  rw [List.mem_flatten] at this
  obtain ⟨l, hl, hxl⟩ := this
  obtain ⟨i, hi, rfl⟩ := List.getElem_of_mem hl
  exact ⟨i, hi, hxl⟩

end Abstract

theorem transGen_cases_head {α : Type} {r : α → α → Prop} {a b : α}
    (h : Relation.TransGen r a b) : r a b ∨ ∃ c, r a c ∧ Relation.TransGen r c b :=
  h.head_induction_on (fun hab => Or.inl hab) (fun h' h _ => Or.inr ⟨_, h', h⟩)

theorem IsEff.needsOf {js : List Job} {j : Job} (h : IsEff js j) : needsOf js j.id = j.needs := by
  unfold IsEff at h
  simp [Dag.needsOf, h]

theorem IsEff.id_mem_ids {js : List Job} {j : Job} (h : IsEff js j) : j.id ∈ ids js :=
  mem_ids.2 ⟨j, List.mem_of_find?_eq_some h, rfl⟩

theorem exists_eff {js : List Job} {x : String} (hx : x ∈ ids js) :
    ∃ j, IsEff js j ∧ j.id = x := by
  obtain ⟨j0, hj0, rfl⟩ := mem_ids.1 hx
  have hs : (js.find? (fun k => k.id == j0.id)).isSome = true :=
    List.find?_isSome.2 ⟨j0, hj0, by simp⟩
  obtain ⟨j, hj⟩ := Option.isSome_iff_exists.1 hs
  have hid : j.id = j0.id := by simpa using List.find?_some hj
  exact ⟨j, by unfold IsEff; rw [hid]; exact hj, hid⟩

section Abstract
variable {js : List Job} {ls : List (List String)}

theorem Peeled.respect (hp : Peeled js ls) (hc : Complete js ls) :
    ∀ j, IsEff js j → ∀ n ∈ j.needs, n.jobId ∈ ids js →
      layerOf ls n.jobId < layerOf ls j.id := by
  intro j hj n hn hny
  obtain ⟨k, hk, hxk⟩ := hp.exists_layer hc hj.id_mem_ids
  rw [hp.layerOf_eq hk hxk]
  have hmem := (hp.mem k hk _).1 hxk
  have hdep : n.jobId ∈ deps js j.id :=
    mem_deps.2 ⟨⟨n, by rw [hj.needsOf]; exact hn, rfl⟩, hny⟩
  have hP := hmem.2.2 _ hdep
  rw [mem_take_flatten] at hP
  obtain ⟨i, hi, hik, hyi⟩ := hP
  rw [hp.layerOf_eq hi hyi]
  exact hik

theorem Peeled.tight (hp : Peeled js ls) :
    ∀ k (hk : k < ls.length) x, x ∈ ls[k] → Chain js x k := by
  intro k
  induction k with
  | zero =>
    intro hk x hx
    exact .nil ((hp.mem 0 hk x).1 hx).1
  | succ k ih =>
    intro hk x hx
    have hk0 : k < ls.length := by omega
    have hmem := (hp.mem (k + 1) hk x).1 hx
    have hxk : x ∉ ls[k] := by
      intro h
      apply hmem.2.1
      rw [mem_take_flatten]
      exact ⟨k, hk0, by omega, h⟩
    have hnotk : ¬ (x ∈ ids js ∧ x ∉ (ls.take k).flatten ∧ ∀ y ∈ deps js x, y ∈ (ls.take k).flatten) :=
      fun h => hxk ((hp.mem k hk0 x).2 h)
    have hxP : x ∉ (ls.take k).flatten := fun h => hmem.2.1 (hp.take_mono (by omega) h)
    have : ∃ y ∈ deps js x, y ∉ (ls.take k).flatten := by
      by_contra hcon
      push Not at hcon
      exact hnotk ⟨hmem.1, hxP, hcon⟩
    obtain ⟨y, hy, hyk⟩ := this
    have hyP := hmem.2.2 y hy
    rw [mem_take_flatten] at hyP
    obtain ⟨i, hi, hik, hyi⟩ := hyP
    have hik' : i = k := by
      by_contra hne
      apply hyk
      rw [mem_take_flatten]
      exact ⟨i, hi, by omega, hyi⟩
    subst hik'
    exact .cons ⟨hmem.1, hy⟩ (ih hk0 y hyi)

theorem Peeled.chain_le (hp : Peeled js ls) :
    ∀ {x n}, Chain js x n → ∀ k (hk : k < ls.length), x ∈ ls[k] → n ≤ k := by
  intro x n hc
  induction hc with
  | nil _ => intro k _ _; exact Nat.zero_le _
  | @cons x y n he _ ih =>
    intro k hk hx
    have hmem := (hp.mem k hk x).1 hx
    have hP := hmem.2.2 y he.2
    rw [mem_take_flatten] at hP
    obtain ⟨i, hi, hik, hyi⟩ := hP
    have := ih i hi hyi
    omega

theorem Peeled.reach (hp : Peeled js ls) :
    ∀ k (hk : k < ls.length) x, x ∈ ls[k] → ∀ y, Relation.TransGen (Edge js) x y →
      y ∈ (ls.take k).flatten := by
  intro k
  induction k using Nat.strong_induction_on with
  | _ k ih =>
    intro hk x hx y hxy
    have hmem := (hp.mem k hk x).1 hx
    rcases transGen_cases_head hxy with he | ⟨c, hxc, hcy⟩
    · exact hmem.2.2 y he.2
    · have hc := hmem.2.2 c hxc.2
      rw [mem_take_flatten] at hc
      obtain ⟨i, hi, hik, hci⟩ := hc
      exact hp.take_mono (by omega) (ih i hik hi c hci y hcy)

theorem Peeled.no_cycle (hp : Peeled js ls) (hc : Complete js ls) : ¬ HasCycle js := by
  rintro ⟨x, hx⟩
  have hxi : x ∈ ids js := by
    rcases transGen_cases_head hx with he | ⟨c, hxc, _⟩
    · exact he.1
    · exact hxc.1
  obtain ⟨k, hk, hxk⟩ := hp.exists_layer hc hxi
  exact ((hp.mem k hk x).1 hxk).2.1 (hp.reach k hk x hxk x hx)

end Abstract

theorem hasDangling_iff {js : List Job} : hasDangling js = true ↔ UnknownNeed js := by
  simp only [hasDangling, List.any_eq_true, Bool.and_eq_true, Bool.not_eq_true']
  constructor
  · rintro ⟨x, hx, n, hn, hopt, hny⟩
    obtain ⟨j, hj, rfl⟩ := exists_eff hx
    exact ⟨j, hj, n, by rwa [← hj.needsOf], by simpa using hopt, by simpa using hny⟩
  · rintro ⟨j, hj, n, hn, hopt, hny⟩
    exact ⟨j.id, hj.id_mem_ids, n, by rw [hj.needsOf]; exact hn, by simpa using hopt, by simpa using hny⟩

theorem edge_wf {js : List Job} (hac : ¬ HasCycle js) :
    WellFounded (fun (y x : {a // a ∈ ids js}) => Edge js x.1 y.1) := by
  let r' : {a // a ∈ ids js} → {a // a ∈ ids js} → Prop :=
    fun y x => Relation.TransGen (Edge js) x.1 y.1
  have : IsTrans _ r' := ⟨fun a b c hab hbc => Relation.TransGen.trans hbc hab⟩
  have : Std.Irrefl r' := ⟨fun a h => hac ⟨a.1, h⟩⟩
  exact Subrelation.wf (fun h => Relation.TransGen.single h)
    (Finite.wellFounded_of_trans_of_irrefl r')

theorem exists_ready {js : List Job} (hac : ¬ HasCycle js) (placed : List String)
    (h : ∃ x ∈ ids js, x ∉ placed) : ∃ x ∈ ids js, ready js placed x = true := by
  obtain ⟨x, hx, hxp⟩ := h
  obtain ⟨a, ha, hmin⟩ := (edge_wf hac).has_min {a : {a // a ∈ ids js} | a.1 ∉ placed} ⟨⟨x, hx⟩, hxp⟩
  refine ⟨a.1, a.2, ready_iff.2 ⟨ha, fun y hy => ?_⟩⟩
  by_contra hyp
  exact hmin ⟨y, (mem_deps.1 hy).2⟩ hyp ⟨a.2, hy⟩

theorem peel_complete {js : List Job} (hac : ¬ HasCycle js) (f : Nat) :
    ∀ (placed : List String),
      ((ids js).filter (fun x => !placed.contains x)).length ≤ f →
      ∀ x ∈ ids js, x ∈ placed ∨ x ∈ (peel js (ids js) f placed).flatten := by
  induction f with
  | zero =>
    intro placed hlen x hx
    left
    have h0 : (ids js).filter (fun x => !placed.contains x) = [] :=
      List.length_eq_zero_iff.1 (by omega)
    have := (List.filter_eq_nil_iff.1 h0) x hx
    simpa using this
  | succ f ih =>
    intro placed hlen x hx
    by_cases hxp : x ∈ placed
    · exact Or.inl hxp
    · right
      obtain ⟨z, hz, hzr⟩ := exists_ready hac placed ⟨x, hx, hxp⟩
      set new := (ids js).filter (ready js placed) with hnew
      have hzn : z ∈ new := List.mem_filter.2 ⟨hz, hzr⟩
      have hne : new.isEmpty = false := by
        cases h : new.isEmpty
        · rfl
        · simp [List.isEmpty_iff] at h; simp [h] at hzn
      have hp : peel js (ids js) (f + 1) placed = new :: peel js (ids js) f (placed ++ new) := by
        simp [peel, ← hnew, hne]
      have hlt : ((ids js).filter (fun x => !(placed ++ new).contains x)).length ≤ f := by
        have heq : (ids js).filter (fun x => !(placed ++ new).contains x)
            = ((ids js).filter (fun x => !placed.contains x)).filter (fun x => !new.contains x) := by
          rw [List.filter_filter]
          congr 1; funext a; simp [Bool.and_comm]
        have hlt' : (((ids js).filter (fun x => !placed.contains x)).filter
            (fun x => !new.contains x)).length
            < ((ids js).filter (fun x => !placed.contains x)).length := by
          rw [List.length_filter_lt_length_iff_exists]
          have hzR : z ∈ (ids js).filter (fun x => !placed.contains x) := by
            simp only [List.mem_filter]
            exact ⟨hz, by simpa using (ready_iff.1 hzr).1⟩
          exact ⟨z, hzR, by simpa using hzn⟩
        rw [heq]; omega
      rw [hp, List.flatten_cons, List.mem_append]
      rcases ih (placed ++ new) hlt x hx with h | h
      · rcases List.mem_append.1 h with h | h
        · exact absurd h hxp
        · exact Or.inl h
      · exact Or.inr h

theorem complete_of_acyclic {js : List Job} (hac : ¬ HasCycle js) :
    Complete js (peel js (ids js) (ids js).length []) := by
  intro x hx
  rcases peel_complete hac (ids js).length [] (by simp) x hx with h | h
  · simp at h
  · exact h

theorem all_iff {js : List Job} {ls : List (List String)} :
    ((ids js).all fun x => ls.flatten.contains x) = true ↔ Complete js ls := by
  simp [Complete, List.all_eq_true]

theorem layers_some {js : List Job} {ls : List (List String)} (h : layers js = some ls) :
    ¬ UnknownNeed js ∧ ls = peel js (ids js) (ids js).length [] ∧ Complete js ls := by
  unfold layers layersE at h
  by_cases hd : hasDangling js = true
  · simp only [hd, ↓reduceIte] at h; simp [Except.toOption] at h
  · simp only [hd, ↓reduceIte, Bool.false_eq_true] at h
    by_cases ha : ((ids js).all fun x => (peel js (ids js) (ids js).length []).flatten.contains x) = true
    · simp only [ha, ↓reduceIte] at h
      simp only [Except.toOption, Option.some.injEq] at h
      subst h
      exact ⟨fun hu => hd (hasDangling_iff.2 hu), rfl, all_iff.1 ha⟩
    · simp only [ha, ↓reduceIte, Bool.false_eq_true] at h; simp [Except.toOption] at h

theorem layers_respect_needs {js : List Job} {ls : List (List String)}
    (h : layers js = some ls) :
    ∀ j, IsEff js j → ∀ n ∈ j.needs, (n.optional = false ∨ n.jobId ∈ ids js) →
      layerOf ls n.jobId < layerOf ls j.id := by
  obtain ⟨hu, rfl, hc⟩ := layers_some h
  intro j hj n hn hopt
  have hny : n.jobId ∈ ids js := by
    rcases hopt with hopt | hy
    · by_contra hn'
      exact hu ⟨j, hj, n, hn, hopt, hn'⟩
    · exact hy
  exact (peel_peeled js _).respect hc j hj n hn hny

theorem layers_none_iff {js : List Job} :
    layers js = none ↔ HasCycle js ∨ UnknownNeed js := by
  unfold layers layersE
  by_cases hd : hasDangling js = true
  · simp only [hd, ↓reduceIte]
    exact ⟨fun _ => Or.inr (hasDangling_iff.1 hd), fun _ => rfl⟩
  · have hu : ¬ UnknownNeed js := fun hu => hd (hasDangling_iff.2 hu)
    simp only [hd, Bool.false_eq_true, ↓reduceIte]
    by_cases ha : ((ids js).all fun x => (peel js (ids js) (ids js).length []).flatten.contains x) = true
    · simp only [ha, ↓reduceIte]
      constructor
      · intro h; simp [Except.toOption] at h
      · rintro (hc | hc)
        · exact absurd hc ((peel_peeled js _).no_cycle (all_iff.1 ha))
        · exact absurd hc hu
    · simp only [ha, Bool.false_eq_true, ↓reduceIte]
      constructor
      · intro _
        left
        by_contra hac
        exact ha (all_iff.2 (complete_of_acyclic hac))
      · intro _; rfl

theorem layers_length {js : List Job} {ls : List (List String)}
    (h : layers js = some ls) (hne : js ≠ []) :
    ls.length = longestPath js + 1 := by
  obtain ⟨_, rfl, hc⟩ := layers_some h
  have hp := peel_peeled js (ids js).length
  generalize peel js (ids js) (ids js).length [] = ls at hp hc
  obtain ⟨j0, hj0⟩ := List.exists_mem_of_ne_nil _ hne
  obtain ⟨k0, hk0, _⟩ := hp.exists_layer hc (mem_ids.2 ⟨j0, hj0, rfl⟩)
  have hlen : 0 < ls.length := by omega
  set m := ls.length - 1 with hm
  have hmlt : m < ls.length := by omega
  obtain ⟨z, hz⟩ := List.exists_mem_of_ne_nil _ (hp.nonempty _ (List.getElem_mem hmlt))
  have hmem : m ∈ {n | ∃ x, Chain js x n} := ⟨z, hp.tight m hmlt z hz⟩
  have hbound : ∀ n ∈ {n | ∃ x, Chain js x n}, n ≤ m := by
    rintro n ⟨x, hx⟩
    have hxi : x ∈ ids js := by
      cases hx with
      | nil h => exact h
      | cons he _ => exact he.1
    obtain ⟨k, hk, hxk⟩ := hp.exists_layer hc hxi
    have := hp.chain_le hx k hk hxk
    omega
  have hlp : longestPath js = m :=
    le_antisymm (csSup_le ⟨m, hmem⟩ hbound) (le_csSup ⟨m, hbound⟩ hmem)
  omega

end Dag
end Pipemaster
