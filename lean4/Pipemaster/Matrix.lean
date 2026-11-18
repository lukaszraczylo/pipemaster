import Mathlib.Data.List.Sort
import Mathlib.Data.String.Basic
import Pipemaster.IR

namespace Pipemaster
namespace Matrix

/-- The value bound to a key; the first binding wins. -/
def lookup (c : Combo) (k : String) : Option String :=
  (c.find? (fun p => p.1 == k)).map Prod.snd

/-- Sets the first binding of the key, or appends one. -/
def setKey (c : Combo) (kv : String × String) : Combo :=
  if c.any (fun p => p.1 == kv.1) then
    let rec go : Combo → Combo
      | [] => []
      | p :: t => if p.1 == kv.1 then (p.1, kv.2) :: t else p :: go t
    go c
  else c ++ [kv]

/-- Stable sort by key. -/
def sortKeys (c : Combo) : Combo :=
  c.mergeSort (fun a b => !decide (b.1 < a.1))

/-- Copies base, applies the pairs of over with later pairs winning, and sorts by key. -/
def overlay (base over : Combo) : Combo :=
  sortKeys (over.foldl setKey base)

/-- Cartesian product in axis order; no axes yields no combos. -/
def product (axes : List Axis) : List Combo :=
  match axes with
  | [] => []
  | _ => axes.foldl (fun res a =>
      res.flatMap fun c => a.values.map fun v => overlay c [(a.name, v)]) [[]]

/-- An exclude entry matches when every pair is present; an empty entry matches nothing. -/
def excluded (excl : List Combo) (c : Combo) : Bool :=
  excl.any fun e => !e.isEmpty && e.all fun kv => lookup c kv.1 == some kv.2

/-- An include agrees with a combo on every original axis key they share. -/
def agrees (orig : List String) (c inc : Combo) : Bool :=
  inc.all fun kv =>
    !orig.contains kv.1 || match lookup c kv.1 with
      | some v => v == kv.2
      | none => true

/-- Names of the original axes. -/
def origNames (m : Matrix) : List String := m.axes.map Axis.name

/-- Product combos that no exclude removes. -/
def survivors (m : Matrix) : List Combo :=
  (product m.axes).filter fun c => !excluded m.excludes c

/-- An include merges into some surviving combo. -/
def matchesAny (m : Matrix) (i : Combo) : Bool :=
  (survivors m).any fun c => agrees (origNames m) c i

/-- A surviving combo with every agreeing include overlaid in order. -/
def mergeInto (m : Matrix) (c0 : Combo) : Combo :=
  (m.includes.filter fun i => agrees (origNames m) c0 i).foldl overlay c0

/-- Mirrors ir.Expand (ir/matrix.go). -/
def expand (m : Matrix) : List Combo :=
  (survivors m).map (mergeInto m)
    ++ (m.includes.filter fun i => !matchesAny m i).map (overlay [])

/-- Membership: a merged surviving combo, or an include that merged into none. -/
theorem expand_mem (m : Matrix) (c : Combo) :
    c ∈ expand m ↔
      (∃ c0 ∈ survivors m, mergeInto m c0 = c) ∨
      (∃ i ∈ m.includes, matchesAny m i = false ∧ overlay [] i = c) := by
  simp [expand, List.mem_append, List.mem_map, List.mem_filter, and_assoc]

/-- Cardinality: one combo per surviving product combo plus one per unmatched include. -/
theorem expand_card (m : Matrix) :
    (expand m).length =
      ((product m.axes).filter (fun c => !excluded m.excludes c)).length
        + (m.includes.filter (fun i => !matchesAny m i)).length := by
  simp [expand, survivors]


/-- Keys never decrease along a combo. -/
def KeySorted (c : Combo) : Prop := c.Pairwise fun a b => ¬ b.1 < a.1

theorem sortKeys_sorted (c : Combo) : KeySorted (sortKeys c) := by
  have := List.pairwise_mergeSort (le := fun a b : String × String => !decide (b.1 < a.1))
    (by
      intro a b c hab hbc
      simp only [Bool.not_eq_true', decide_eq_false_iff_not, not_lt] at hab hbc ⊢
      exact le_trans hab hbc)
    (by
      intro a b
      by_cases h : b.1 < a.1
      · have := lt_asymm h; simp [this]
      · simp [h]) c
  simpa [KeySorted, sortKeys] using this

theorem overlay_sorted (b o : Combo) : KeySorted (overlay b o) := sortKeys_sorted _

theorem foldl_overlay_sorted (l : List Combo) :
    ∀ c0 : Combo, KeySorted c0 → KeySorted (l.foldl overlay c0) := by
  induction l with
  | nil => intro c0 h; exact h
  | cons i t ih => intro c0 _; exact ih _ (overlay_sorted _ _)

theorem product_sorted (axes : List Axis) : ∀ c ∈ product axes, KeySorted c := by
  have key : ∀ (axes : List Axis) (res : List Combo), (∀ c ∈ res, KeySorted c) →
      ∀ c ∈ axes.foldl (fun res a =>
        res.flatMap fun c => a.values.map fun v => overlay c [(a.name, v)]) res, KeySorted c := by
    intro axes
    induction axes with
    | nil => intro res h; exact h
    | cons a t ih =>
      intro res _
      apply ih
      intro c hc
      simp only [List.mem_flatMap, List.mem_map] at hc
      obtain ⟨_, _, v, _, rfl⟩ := hc
      exact overlay_sorted _ _
  cases axes with
  | nil => simp [product]
  | cons a t =>
    exact key (a :: t) [[]] (by intro c hc; simp at hc; subst hc; simp [KeySorted])

/-- Every expanded combo is sorted by key. -/
theorem expand_sorted (m : Matrix) : ∀ c ∈ expand m, KeySorted c := by
  intro c hc
  rcases (expand_mem m c).1 hc with ⟨c0, h0, rfl⟩ | ⟨i, _, _, rfl⟩
  · exact foldl_overlay_sorted _ c0 (product_sorted _ c0 (List.mem_filter.1 h0).1)
  · exact overlay_sorted _ _

/-- With no axes every include is appended. -/
theorem expand_no_axes (m : Matrix) (h : m.axes = []) :
    expand m = m.includes.map (overlay []) := by
  simp [expand, survivors, matchesAny, product, h]

end Matrix
end Pipemaster
