namespace Pipemaster

/-- A needs edge; an optional edge to a missing job is ignored. -/
structure Need where
  jobId : String
  optional : Bool
  deriving DecidableEq, Repr

/-- The slice of an IR job the DAG transform reads. -/
structure Job where
  id : String
  needs : List Need
  deriving DecidableEq, Repr

/-- Verdict levels, best to worst; the order matches caps.Level. -/
inductive Level
  | native | emulated | external | lost
  deriving DecidableEq, Repr

namespace Level

def toNat : Level → Nat
  | native => 0
  | emulated => 1
  | external => 2
  | lost => 3

theorem toNat_injective : Function.Injective toNat := by
  intro a b h; cases a <;> cases b <;> first | rfl | (simp [toNat] at h)

def ofRank : Nat → Level
  | 0 => native
  | 1 => emulated
  | 2 => external
  | _ => lost

theorem toNat_le_three (l : Level) : l.toNat ≤ 3 := by cases l <;> simp [toNat]

theorem ofRank_toNat (l : Level) : ofRank l.toNat = l := by cases l <;> rfl

theorem toNat_ofRank {n : Nat} (h : n ≤ 3) : (ofRank n).toNat = n := by
  match n, h with
  | 0, _ | 1, _ | 2, _ | 3, _ => rfl

/-- The worse of two levels. -/
def worse (a b : Level) : Level := ofRank (Nat.max a.toNat b.toNat)

theorem toNat_worse (a b : Level) : (worse a b).toNat = Nat.max a.toNat b.toNat := by
  apply toNat_ofRank
  have := toNat_le_three a
  have := toNat_le_three b
  exact Nat.max_le.2 ⟨by assumption, by assumption⟩

end Level

/-- Feature names are opaque to the model. -/
abbrev Feature := String

/-- Mirrors ir.FeatureUse (ir/features.go). -/
structure FeatureUse where
  irPath : String
  feature : Feature
  deriving DecidableEq, Repr

/-- A target's verdict for one feature (caps.Support). -/
structure Support where
  level : Level
  strategy : String
  caveat : String
  deriving DecidableEq, Repr

/-- A target capability matrix (caps.Matrix); the first binding of a feature wins. -/
abbrev CapsMatrix := List (Feature × Support)

/-- Undeclared features are lost so omissions never pass silently. -/
def CapsMatrix.lookup (m : CapsMatrix) (f : Feature) : Support :=
  match m.find? (fun p => p.1 == f) with
  | some p => p.2
  | none => { level := .lost, strategy := "", caveat := "not declared by target" }

/-- One matrix axis. -/
structure Axis where
  name : String
  values : List String
  deriving DecidableEq, Repr

/-- A matrix combination as key/value pairs, sorted by key (ir.Combo). -/
abbrev Combo := List (String × String)

/-- A job fan-out over axes. -/
structure Matrix where
  axes : List Axis
  includes : List Combo
  excludes : List Combo
  deriving DecidableEq, Repr

end Pipemaster
