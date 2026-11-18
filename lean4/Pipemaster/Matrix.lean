import Mathlib.Data.List.Sort
import Mathlib.Data.String.Basic
import Mathlib.Data.List.Induction
import Pipemaster.IR

namespace Pipemaster
namespace Matrix

/-- The value bound to a key; the first binding wins. -/
def lookup (c : Combo) (k : String) : Option String :=
  (c.find? (fun p => p.1 == k)).map Prod.snd

/-- Sets the binding of the key, or appends one; combos hold each key once. -/
def setKey (c : Combo) (kv : String × String) : Combo :=
  if c.any (fun p => p.1 == kv.1) then
    c.map fun p => if p.1 == kv.1 then (p.1, kv.2) else p
  else c ++ [kv]

/-- Stable sort by key. -/
def sortKeys (c : Combo) : Combo :=
  c.mergeSort (fun a b => !decide (b.1 < a.1))

/-- Copies base, applies the pairs of over with later pairs winning, and sorts by key. -/
def overlay (base over : Combo) : Combo :=
  sortKeys (over.foldl setKey base)

/-- One axis step of the product. -/
def prodStep (res : List Combo) (a : Axis) : List Combo :=
  res.flatMap fun c => a.values.map fun v => overlay c [(a.name, v)]

/-- Cartesian product in axis order; no axes yields no combos. -/
def product (axes : List Axis) : List Combo :=
  match axes with
  | [] => []
  | _ => axes.foldl prodStep [[]]

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
      ∀ c ∈ axes.foldl prodStep res, KeySorted c := by
    intro axes
    induction axes with
    | nil => intro res h; exact h
    | cons a t ih =>
      intro res _
      apply ih
      intro c hc
      simp only [prodStep, List.mem_flatMap, List.mem_map] at hc
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

/-! ### Semantics of overlay, exclude, include -/

/-- Each key occurs once. -/
def KeysNodup (c : Combo) : Prop := (c.map Prod.fst).Nodup

/-- The key is bound. -/
def HasKey (c : Combo) (k : String) : Prop := ∃ v, (k, v) ∈ c

/-- The value that the last binding of the key in o gives. -/
def overVal : Combo → String → Option String
  | [], _ => none
  | p :: t, k =>
    match overVal t k with
    | some w => some w
    | none => if p.1 == k then some p.2 else none

theorem overVal_mem : ∀ {o : Combo} {k w}, overVal o k = some w → (k, w) ∈ o := by
  intro o
  induction o with
  | nil => intro k w h; simp [overVal] at h
  | cons p t ih =>
    intro k w h
    simp only [overVal] at h
    cases ht : overVal t k with
    | some x => rw [ht] at h; cases h; exact List.mem_cons_of_mem _ (ih ht)
    | none =>
      rw [ht] at h
      by_cases hp : p.1 == k
      · simp [hp] at h; subst h; simp at hp; subst hp; exact List.mem_cons_self ..
      · simp [hp] at h

theorem overVal_none : ∀ {o : Combo} {k}, k ∉ o.map Prod.fst → overVal o k = none := by
  intro o
  induction o with
  | nil => intro k _; rfl
  | cons p t ih =>
    intro k h
    simp only [List.map_cons, List.mem_cons, not_or] at h
    simp only [overVal, ih h.2]
    have : ¬ (p.1 == k) = true := by simpa using fun e => h.1 e.symm
    simp [this]

theorem overVal_some_of_mem : ∀ {o : Combo} {k}, k ∈ o.map Prod.fst → ∃ w, overVal o k = some w := by
  intro o k h
  by_contra hc
  push Not at hc
  have : overVal o k = none := by
    cases e : overVal o k with
    | none => rfl
    | some w => exact absurd e (hc w)
  obtain ⟨p, hp, rfl⟩ := List.mem_map.1 h
  have hm : ∀ {o : Combo}, overVal o p.1 = none → p ∉ o := by
    intro o
    induction o with
    | nil => simp
    | cons q t ih =>
      intro h0
      simp only [overVal] at h0
      cases ht : overVal t p.1 with
      | some x => rw [ht] at h0; cases h0
      | none =>
        rw [ht] at h0
        by_cases hq : q.1 == p.1
        · simp [hq] at h0
        · intro hin
          rcases List.mem_cons.1 hin with rfl | hin
          · simp at hq
          · exact ih ht hin
  exact hm this hp

theorem overVal_of_nodup : ∀ {o : Combo}, (o.map Prod.fst).Nodup → ∀ p ∈ o, overVal o p.1 = some p.2 := by
  intro o
  induction o with
  | nil => intro _ p hp; simp at hp
  | cons q t ih =>
    intro hnd p hp
    rw [List.map_cons, List.nodup_cons] at hnd
    rcases List.mem_cons.1 hp with rfl | hp
    · have := overVal_none (o := t) (k := p.1) hnd.1
      simp [overVal, this]
    · simp [overVal, ih hnd.2 p hp]

theorem mem_setKey {c : Combo} {kv : String × String} {k v : String} :
    (k, v) ∈ setKey c kv ↔ (k = kv.1 ∧ v = kv.2) ∨ (k ≠ kv.1 ∧ (k, v) ∈ c) := by
  unfold setKey
  by_cases h : c.any (fun p => p.1 == kv.1) = true
  · simp only [h, ↓reduceIte, List.mem_map]
    obtain ⟨x, hx, hxe⟩ := List.any_eq_true.1 h
    have hxe' : x.1 = kv.1 := by simpa using hxe
    constructor
    · rintro ⟨y, hy, hye⟩
      by_cases hu : y.1 = kv.1
      · left; simp [hu] at hye; subst hye; exact ⟨rfl, rfl⟩
      · right; simp [hu] at hye; subst hye; exact ⟨hu, hy⟩
    · rintro (⟨rfl, rfl⟩ | ⟨hne, hm⟩)
      · exact ⟨x, hx, by simp [hxe']⟩
      · exact ⟨(k, v), hm, by simp [hne]⟩
  · simp only [h, Bool.false_eq_true, ↓reduceIte, List.mem_append, List.mem_singleton]
    have h' : ∀ x ∈ c, x.1 ≠ kv.1 := by
      intro x hx hxe
      exact h (List.any_eq_true.2 ⟨x, hx, by simpa using hxe⟩)
    constructor
    · rintro (hm | he)
      · exact Or.inr ⟨fun e => h' _ hm e, hm⟩
      · subst he; exact Or.inl ⟨rfl, rfl⟩
    · rintro (⟨rfl, rfl⟩ | ⟨_, hm⟩)
      · exact Or.inr rfl
      · exact Or.inl hm

theorem setKey_nodup {c : Combo} (kv : String × String) (h : KeysNodup c) :
    KeysNodup (setKey c kv) := by
  unfold setKey KeysNodup at *
  split
  · have : (c.map fun p => if p.1 == kv.1 then (p.1, kv.2) else p).map Prod.fst = c.map Prod.fst := by
      simp only [List.map_map]
      apply List.map_congr_left
      intro p _
      show (if (p.1 == kv.1) = true then (p.1, kv.2) else p).1 = p.1
      split <;> rfl
    rw [this]; exact h
  · rename_i hn
    simp only [List.map_append, List.map_singleton]
    rw [List.nodup_append]
    refine ⟨h, by simp, ?_⟩
    intro a ha b hb hab
    simp only [List.mem_singleton] at hb
    subst hb
    obtain ⟨p, hp, rfl⟩ := List.mem_map.1 ha
    apply hn
    exact List.any_eq_true.2 ⟨p, hp, by simpa using hab⟩

theorem foldl_setKey : ∀ (o : Combo) (b : Combo), KeysNodup b →
    KeysNodup (o.foldl setKey b) ∧ ∀ k v, (k, v) ∈ o.foldl setKey b ↔
      (match overVal o k with | some w => v = w | none => (k, v) ∈ b) := by
  intro o
  induction o with
  | nil => intro b h; exact ⟨h, by simp [overVal]⟩
  | cons p t ih =>
    intro b h
    obtain ⟨hn, hm⟩ := ih (setKey b p) (setKey_nodup p h)
    refine ⟨hn, fun k v => ?_⟩
    rw [List.foldl_cons, hm]
    simp only [overVal]
    cases ht : overVal t k with
    | some w => simp
    | none =>
      simp only [mem_setKey]
      by_cases hp : p.1 == k
      · have : p.1 = k := by simpa using hp
        subst this
        simp
      · have : k ≠ p.1 := fun e => hp (by simp [e])
        simp [hp, this]

theorem sortKeys_nodup {c : Combo} (h : KeysNodup c) : KeysNodup (sortKeys c) :=
  ((List.mergeSort_perm c _).map Prod.fst).nodup_iff.2 h

theorem mem_sortKeys {c : Combo} {x : String × String} : x ∈ sortKeys c ↔ x ∈ c :=
  List.mem_mergeSort

theorem overlay_nodup {b : Combo} (o : Combo) (h : KeysNodup b) : KeysNodup (overlay b o) :=
  sortKeys_nodup ((foldl_setKey o b h).1)

/-- Overlay binds a key to its last value in o, else keeps the base binding. -/
theorem mem_overlay {b : Combo} (o : Combo) (h : KeysNodup b) (k v : String) :
    (k, v) ∈ overlay b o ↔ (match overVal o k with | some w => v = w | none => (k, v) ∈ b) := by
  unfold overlay
  rw [mem_sortKeys]
  exact (foldl_setKey o b h).2 k v

theorem nodup_key_eq {c : Combo} (h : KeysNodup c) {k v w : String}
    (hv : (k, v) ∈ c) (hw : (k, w) ∈ c) : v = w := by
  have := List.inj_on_of_nodup_map h hv hw rfl
  simpa using this

theorem lookup_eq_some {c : Combo} (h : KeysNodup c) {k v : String} :
    lookup c k = some v ↔ (k, v) ∈ c := by
  unfold lookup
  constructor
  · intro hl
    obtain ⟨p, hp, rfl⟩ := Option.map_eq_some_iff.1 hl
    have hk : p.1 = k := by simpa using List.find?_some hp
    have hm := List.mem_of_find?_eq_some hp
    rw [← hk]; exact hm
  · intro hm
    have hs : (c.find? (fun p => p.1 == k)).isSome = true :=
      List.find?_isSome.2 ⟨(k, v), hm, by simp⟩
    obtain ⟨p, hp⟩ := Option.isSome_iff_exists.1 hs
    have hk : p.1 = k := by simpa using List.find?_some hp
    have hpm := List.mem_of_find?_eq_some hp
    have : p.2 = v := by
      have hpm' : (k, p.2) ∈ c := by rw [← hk]; exact hpm
      exact nodup_key_eq h hpm' hm
    simp [hp, this]

/-- Exclude semantics: some non-empty entry has every pair present. -/
theorem excluded_iff (excl : List Combo) (c : Combo) :
    excluded excl c = true ↔
      ∃ e ∈ excl, e ≠ [] ∧ ∀ kv ∈ e, lookup c kv.1 = some kv.2 := by
  simp [excluded]

theorem mem_survivors (m : Matrix) (c : Combo) :
    c ∈ survivors m ↔ c ∈ product m.axes ∧
      ¬ ∃ e ∈ m.excludes, e ≠ [] ∧ ∀ kv ∈ e, lookup c kv.1 = some kv.2 := by
  have := excluded_iff m.excludes c
  simp only [survivors, List.mem_filter, Bool.not_eq_true']
  constructor
  · rintro ⟨h1, h2⟩
    refine ⟨h1, fun h => ?_⟩
    rw [← this] at h; simp [h] at h2
  · rintro ⟨h1, h2⟩
    refine ⟨h1, ?_⟩
    by_contra hc
    exact h2 (this.1 (by simpa using hc))

/-! ### Product combos -/

/-- A well-formed combo binds each key once and every original axis. -/
def Good (axes : List Axis) (c : Combo) : Prop :=
  KeysNodup c ∧ ∀ a ∈ axes, HasKey c a.name

theorem product_good (axes : List Axis) : ∀ c ∈ product axes, Good axes c := by
  have key : ∀ (axes : List Axis), ∀ c ∈ axes.foldl prodStep [[]], Good axes c := by
    intro axes
    induction axes using List.reverseRecOn with
    | nil => intro c hc; simp at hc; subst hc; exact ⟨by simp [KeysNodup], by simp⟩
    | append_singleton ax a ih =>
      intro c hc
      rw [List.foldl_append] at hc
      simp only [List.foldl_cons, List.foldl_nil, prodStep, List.mem_flatMap, List.mem_map] at hc
      obtain ⟨c1, hc1, v, hv, rfl⟩ := hc
      obtain ⟨hnd, hk⟩ := ih c1 hc1
      refine ⟨overlay_nodup _ hnd, fun b hb => ?_⟩
      rcases List.mem_append.1 hb with hb | hb
      · obtain ⟨w, hw⟩ := hk b hb
        by_cases hn : b.name = a.name
        · refine ⟨v, (mem_overlay _ hnd _ _).2 ?_⟩
          simp [overVal, hn]
        · refine ⟨w, (mem_overlay _ hnd _ _).2 ?_⟩
          have : overVal [(a.name, v)] b.name = none := by
            apply overVal_none; simpa using fun e => hn e
          simp [this, hw]
      · have : b = a := by simpa using hb
        subst this
        exact ⟨v, (mem_overlay _ hnd _ _).2 (by simp [overVal])⟩
  cases axes with
  | nil => simp [product]
  | cons a t => exact key (a :: t)

/-! ### Include merging -/

/-- An agreeing include's pairs on original keys are already in the combo. -/
theorem agrees_pairs {orig : List String} {c inc : Combo} (hnd : KeysNodup c)
    (hk : ∀ k ∈ orig, HasKey c k) (h : agrees orig c inc = true) :
    ∀ p ∈ inc, p.1 ∈ orig → p ∈ c := by
  intro p hp hpo
  unfold agrees at h
  have := List.all_eq_true.1 h p hp
  obtain ⟨w, hw⟩ := hk _ hpo
  have hl := (lookup_eq_some hnd).2 hw
  simp [hpo, hl] at this
  have : w = p.2 := this
  subst this
  exact hw

theorem overlay_orig {orig : List String} {acc inc : Combo} (hnd : KeysNodup acc)
    (hp : ∀ p ∈ inc, p.1 ∈ orig → p ∈ acc) :
    ∀ k ∈ orig, ∀ v, (k, v) ∈ overlay acc inc ↔ (k, v) ∈ acc := by
  intro k hk v
  rw [mem_overlay _ hnd]
  by_cases hm : k ∈ inc.map Prod.fst
  · obtain ⟨w, hw⟩ := overVal_some_of_mem hm
    have h1 := hp _ (overVal_mem hw) hk
    rw [hw]
    simp only
    constructor
    · rintro rfl; exact h1
    · intro h2; exact nodup_key_eq hnd h2 h1
  · rw [overVal_none hm]

theorem foldl_overlay_nodup (F : List Combo) :
    ∀ acc : Combo, KeysNodup acc → KeysNodup (F.foldl overlay acc) := by
  induction F with
  | nil => intro acc h; exact h
  | cons i t ih => intro acc h; exact ih _ (overlay_nodup _ h)

theorem foldl_overlay_orig {orig : List String} {c0 : Combo} (F : List Combo)
    (hF : ∀ i ∈ F, ∀ p ∈ i, p.1 ∈ orig → p ∈ c0) :
    ∀ acc : Combo, KeysNodup acc → (∀ k ∈ orig, ∀ v, (k, v) ∈ acc ↔ (k, v) ∈ c0) →
      ∀ k ∈ orig, ∀ v, (k, v) ∈ F.foldl overlay acc ↔ (k, v) ∈ c0 := by
  induction F with
  | nil => intro acc _ h; simpa using h
  | cons i t ih =>
    intro acc hnd h
    apply ih (fun j hj => hF j (List.mem_cons_of_mem _ hj)) _ (overlay_nodup _ hnd)
    intro k hk v
    rw [overlay_orig hnd (fun p hp hpo => (h _ hpo p.2).2 (hF i (List.mem_cons_self ..) p hp hpo)) k hk v]
    exact h k hk v

theorem foldl_overlay_keeps {k v : String} (F : List Combo) (hF : ∀ i ∈ F, k ∉ i.map Prod.fst) :
    ∀ acc : Combo, KeysNodup acc → (k, v) ∈ acc → (k, v) ∈ F.foldl overlay acc := by
  induction F with
  | nil => intro acc _ h; exact h
  | cons i t ih =>
    intro acc hnd h
    apply ih (fun j hj => hF j (List.mem_cons_of_mem _ hj)) _ (overlay_nodup _ hnd)
    rw [mem_overlay _ hnd, overVal_none (hF i (List.mem_cons_self ..))]
    exact h

theorem survivor_good (m : Matrix) {c0 : Combo} (h : c0 ∈ survivors m) :
    KeysNodup c0 ∧ ∀ k ∈ origNames m, HasKey c0 k := by
  obtain ⟨hn, hk⟩ := product_good _ c0 (List.mem_filter.1 h).1
  refine ⟨hn, fun k hko => ?_⟩
  obtain ⟨a, ha, rfl⟩ := List.mem_map.1 hko
  exact hk a ha

/-- A merge never changes original axis values. -/
theorem mergeInto_orig (m : Matrix) {c0 : Combo} (h : c0 ∈ survivors m) :
    KeysNodup (mergeInto m c0) ∧
      ∀ k ∈ origNames m, ∀ v, (k, v) ∈ mergeInto m c0 ↔ (k, v) ∈ c0 := by
  obtain ⟨hn, hk⟩ := survivor_good m h
  refine ⟨foldl_overlay_nodup _ _ hn, ?_⟩
  apply foldl_overlay_orig _ _ c0 hn (fun _ _ _ => Iff.rfl)
  intro i hi
  exact agrees_pairs hn hk (List.mem_filter.1 hi).2

/-! ### The substantive expand theorems -/

/-- (a) Every output combo derives from a surviving product combo it agrees with on every
original axis key, or is an include that merged into no survivor. -/
theorem expand_combo_origin (m : Matrix) :
    ∀ c ∈ expand m,
      (∃ c0, c0 ∈ product m.axes ∧ excluded m.excludes c0 = false ∧
          (∀ a ∈ m.axes, HasKey c a.name) ∧
          ∀ k ∈ origNames m, ∀ v, (k, v) ∈ c ↔ (k, v) ∈ c0) ∨
      (∃ i ∈ m.includes, matchesAny m i = false ∧ c = overlay [] i) := by
  intro c hc
  rcases (expand_mem m c).1 hc with ⟨c0, h0, rfl⟩ | ⟨i, hi, hm, rfl⟩
  · left
    have hs := List.mem_filter.1 h0
    refine ⟨c0, hs.1, by simpa using hs.2, ?_, (mergeInto_orig m h0).2⟩
    intro a ha
    obtain ⟨w, hw⟩ := (survivor_good m h0).2 a.name (List.mem_map.2 ⟨a, ha, rfl⟩)
    exact ⟨w, (((mergeInto_orig m h0).2 a.name (List.mem_map.2 ⟨a, ha, rfl⟩) w).2 hw)⟩
  · right; exact ⟨i, hi, hm, rfl⟩

/-- (b) The i-th surviving product combo yields the i-th output combo with equal original
axis values. -/
theorem expand_survivor_preserved (m : Matrix) (i : Nat) (hi : i < (survivors m).length) :
    ∃ hi' : i < (expand m).length,
      ∀ k ∈ origNames m, ∀ v, (k, v) ∈ (expand m)[i] ↔ (k, v) ∈ (survivors m)[i] := by
  have hlen : i < (expand m).length := by
    simp only [expand, List.length_append, List.length_map]; omega
  refine ⟨hlen, ?_⟩
  have heq : (expand m)[i] = mergeInto m (survivors m)[i] := by
    simp only [expand]
    rw [List.getElem_append_left (by simpa using hi)]
    simp
  rw [heq]
  exact (mergeInto_orig m (List.getElem_mem hi)).2

/-- (c1) An include that agrees with a survivor merges into it. Original-key pairs always
appear; a pair on any other key appears unless a later agreeing include binds that key. -/
theorem expand_include_merged (m : Matrix) {c0 : Combo} (h0 : c0 ∈ survivors m)
    {pre post : List Combo} {i : Combo} (hdec : m.includes = pre ++ i :: post)
    (hag : agrees (origNames m) c0 i = true) (hnd : (i.map Prod.fst).Nodup)
    (hlater : ∀ p ∈ i, p.1 ∉ origNames m →
      ∀ i' ∈ post, agrees (origNames m) c0 i' = true → p.1 ∉ i'.map Prod.fst) :
    mergeInto m c0 ∈ expand m ∧ ∀ p ∈ i, p ∈ mergeInto m c0 := by
  refine ⟨(expand_mem m _).2 (Or.inl ⟨c0, h0, rfl⟩), fun p hp => ?_⟩
  obtain ⟨hn, hk⟩ := survivor_good m h0
  by_cases hpo : p.1 ∈ origNames m
  · exact ((mergeInto_orig m h0).2 p.1 hpo p.2).2 (agrees_pairs hn hk hag p hp hpo)
  · unfold mergeInto
    rw [hdec, List.filter_append, List.filter_cons_of_pos hag, List.foldl_append, List.foldl_cons]
    set A := List.foldl overlay c0 (List.filter (fun i => agrees (origNames m) c0 i) pre) with hA
    have hAn : KeysNodup A := foldl_overlay_nodup _ _ hn
    have hpm : p ∈ overlay A i := by
      have := overVal_of_nodup hnd p hp
      have h2 := (mem_overlay i hAn p.1 p.2).2 (by simp [this])
      exact h2
    apply foldl_overlay_keeps (k := p.1) (v := p.2) _ _ _ (overlay_nodup _ hAn) hpm
    intro j hj
    obtain ⟨hjp, hja⟩ := List.mem_filter.1 hj
    exact hlater p hp hpo j hjp hja

/-- (c2) An include that merged into no survivor is an output combo. -/
theorem expand_include_appended (m : Matrix) {i : Combo} (hi : i ∈ m.includes)
    (hm : matchesAny m i = false) :
    overlay [] i ∈ expand m ∧ ∀ p ∈ overlay [] i, p ∈ i := by
  refine ⟨(expand_mem m _).2 (Or.inr ⟨i, hi, hm, rfl⟩), fun p hp => ?_⟩
  obtain ⟨k, v⟩ := p
  rw [mem_overlay _ (by simp [KeysNodup])] at hp
  cases hv : overVal i k with
  | none => simp [hv] at hp
  | some w =>
    rw [hv] at hp
    simp only at hp
    subst hp
    exact overVal_mem hv

/-- (c) Every include is either merged into some survivor or appears as an output combo. -/
theorem expand_include_accounted (m : Matrix) :
    ∀ i ∈ m.includes,
      (∃ c0 ∈ survivors m, agrees (origNames m) c0 i = true ∧ mergeInto m c0 ∈ expand m) ∨
      overlay [] i ∈ expand m := by
  intro i hi
  by_cases hm : matchesAny m i = true
  · left
    obtain ⟨c0, h0, ha⟩ := List.any_eq_true.1 (by simpa [matchesAny] using hm)
    exact ⟨c0, h0, ha, (expand_mem m _).2 (Or.inl ⟨c0, h0, rfl⟩)⟩
  · right
    exact (expand_include_appended m hi (by simpa using hm)).1

end Matrix
end Pipemaster
