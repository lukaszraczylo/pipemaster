import Pipemaster.Translate

namespace Pipemaster
namespace TranslateClass

open Pipemaster.Expr Pipemaster.Translate

/-! Dialect classes (expr.Class, expr/lang.go). A class is a semantics: `ghaView` and `mapEnv` are the
class representatives, and a dialect of the class differs only in the names its vocabulary gives the
canonical refs. Translation works on canonical refs, so a same-class translation renames refs through the
two vocabularies and changes no node. -/

/-- The canonical refs an expression reads. -/
def refs : E → List Ref
  | .ref r => [r]
  | .not x => refs x
  | .and l r => refs l ++ refs r
  | .or l r => refs l ++ refs r
  | .eq l r => refs l ++ refs r
  | .ne l r => refs l ++ refs r
  | .lt l r => refs l ++ refs r
  | .startsWith a b => refs a ++ refs b
  | .endsWith a b => refs a ++ refs b
  | .contains a b => refs a ++ refs b
  | .call _ a => refs a
  | .rx x _ _ => refs x
  | .nrx x _ _ => refs x
  | .rawRx x _ => refs x
  | _ => []

/-- The vocabulary `nm` names every canonical ref of `e` (Mirrors expr.Vocab (expr/vocab.go)). -/
def named (nm : Ref → Bool) (e : E) : Bool := (refs e).all nm

/-- Evaluation reads a view only at the refs of the expression. -/
theorem eval_congr (sem : Sem) {v w : View} :
    ∀ e : E, (∀ r ∈ refs e, v r = w r) → eval sem v e = eval sem w e := by
  intro e
  induction e with
  | ref r => intro h; simp [eval, refV, h r (by simp [refs])]
  | not x ih => intro h; simp only [eval]; rw [ih h]
  | and l r ihl ihr =>
    intro h
    simp only [refs, List.mem_append] at h
    simp only [eval]
    rw [ihl (fun r hr => h r (Or.inl hr)), ihr (fun r hr => h r (Or.inr hr))]
  | or l r ihl ihr =>
    intro h
    simp only [refs, List.mem_append] at h
    simp only [eval]
    rw [ihl (fun r hr => h r (Or.inl hr)), ihr (fun r hr => h r (Or.inr hr))]
  | eq l r ihl ihr =>
    intro h
    simp only [refs, List.mem_append] at h
    simp only [eval]
    rw [ihl (fun r hr => h r (Or.inl hr)), ihr (fun r hr => h r (Or.inr hr))]
  | ne l r ihl ihr =>
    intro h
    simp only [refs, List.mem_append] at h
    simp only [eval]
    rw [ihl (fun r hr => h r (Or.inl hr)), ihr (fun r hr => h r (Or.inr hr))]
  | startsWith l r ihl ihr =>
    intro h
    simp only [refs, List.mem_append] at h
    simp only [eval]
    rw [ihl (fun r hr => h r (Or.inl hr)), ihr (fun r hr => h r (Or.inr hr))]
  | endsWith l r ihl ihr =>
    intro h
    simp only [refs, List.mem_append] at h
    simp only [eval]
    rw [ihl (fun r hr => h r (Or.inl hr)), ihr (fun r hr => h r (Or.inr hr))]
  | contains l r ihl ihr =>
    intro h
    simp only [refs, List.mem_append] at h
    simp only [eval]
    rw [ihl (fun r hr => h r (Or.inl hr)), ihr (fun r hr => h r (Or.inr hr))]
  | rx x m s ih => intro h; simp only [eval]; rw [ih h]
  | nrx x m s ih => intro h; simp only [eval]; rw [ih h]
  | _ => intro _; simp [eval]

/-- Renaming the refs of an expression between two dialects of one class keeps its truthiness: when both
vocabularies name every ref, and the two environments agree on the canonical refs both name. -/
theorem rename_sound (sem : Sem) (nA nB : Ref → Bool) (vA vB : View) (e : E)
    (hA : named nA e = true) (hB : named nB e = true)
    (hrel : ∀ r, nA r = true → nB r = true → vA r = vB r) :
    holds sem vA e = holds sem vB e := by
  unfold holds
  rw [eval_congr sem e (fun r hr => hrel r (List.all_eq_true.mp hA r hr) (List.all_eq_true.mp hB r hr))]

/-- `tr_sound` for any dialect of the GHA class and any of the GitLab class: each dialect's view agrees with
its class representative on the refs the expression reads. -/
theorem tr_sound_dialect {e : E} (env : Env) (vA vB : View) (h : Translatable e)
    (hA : ∀ r ∈ refs e, vA r = ghaView env r)
    (hB : ∀ e', tr e = some e' → ∀ r ∈ refs e', vB r = mapEnv env r) :
    ∃ e', tr e = some e' ∧ holds .gitlab vB e' = holds .gha vA e := by
  obtain ⟨e', he', hh⟩ := tr_sound env h
  refine ⟨e', he', ?_⟩
  unfold holds at hh ⊢
  rw [eval_congr .gitlab _ (hB e' he'), hh, eval_congr .gha _ hA]

/-- `tr_sound_rev` for any dialect of the GitLab class and any of the GHA class. -/
theorem tr_sound_rev_dialect {e : E} (env : Env) (vA vB : View) (h : TranslatableR e)
    (hB : ∀ r ∈ refs e, vB r = mapEnv env r)
    (hA : ∀ e', trR e = some e' → ∀ r ∈ refs e', vA r = ghaView env r) :
    ∃ e', trR e = some e' ∧ holds .gha vA e' = holds .gitlab vB e := by
  obtain ⟨e', he', hh⟩ := tr_sound_rev env h
  refine ⟨e', he', ?_⟩
  unfold holds at hh ⊢
  rw [eval_congr .gha _ (hA e' he'), hh, eval_congr .gitlab _ hB]

end TranslateClass
end Pipemaster
