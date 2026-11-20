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

/-! ### Event literals

Event values are native strings in the AST, so a same-class rename also maps them from one Events table to
the other (Mirrors expr.Dialect (expr/translate.go): the Events field). The translator renames a literal only
when both tables are injective after lowering and name no event with an empty string. -/

/-- A dialect's event table: canonical event to the string the dialect reports. -/
structure EvTab (t : Ev → String) : Prop where
  inj : ∀ a b, lower (t a) = lower (t b) → a = b
  ne : ∀ e, lower (t e) ≠ ""

/-- The string comparison the class applies to `==`: GHA folds case, GitLab does not. -/
def foldS : Sem → String → String
  | .gha, s => lower s
  | .gitlab, s => s

theorem eqV_str (sem : Sem) (x y : String) : eqV sem (.str x) (.str y) = (foldS sem x == foldS sem y) := by
  cases sem <;> simp [eqV, foldS]

theorem foldS_empty (sem : Sem) : foldS sem "" = "" := by cases sem <;> simp [foldS, lower_empty]

theorem evTab_ne {t : Ev → String} (h : EvTab t) (e : Ev) : t e ≠ "" := fun he => h.ne e (by rw [he, lower_empty])

theorem fold_ne {t : Ev → String} (h : EvTab t) (sem : Sem) (e : Ev) : foldS sem (t e) ≠ "" := by
  cases sem
  · exact h.ne e
  · exact evTab_ne h e

/-- Two events share a folded string exactly when they are the same event. -/
theorem fold_beq {t : Ev → String} (h : EvTab t) (sem : Sem) (a b : Ev) :
    (foldS sem (t a) == foldS sem (t b)) = decide (a = b) := by
  by_cases hab : a = b
  · subst hab; simp
  · have hne : foldS sem (t a) ≠ foldS sem (t b) := by
      intro heq
      apply hab
      apply h.inj
      cases sem
      · exact heq
      · exact congrArg lower heq
    simp [hab, hne]

theorem eqV_null (sem : Sem) {y : String} (hy : foldS sem y ≠ "") : eqV sem .null (.str y) = false := by
  cases sem
  · have : y ≠ "" := fun h => hy (by rw [h]; exact lower_empty)
    simp [eqV, this]
  · simp [eqV]

/-- The comparison `event == literal` gives the same boolean on both tables, on every event. -/
theorem event_cmp (sem : Sem) {tA tB : Ev → String} (hA : EvTab tA) (hB : EvTab tB) (env : Env)
    {vA vB : View} (ha : vA .event = env.event.map tA) (hb : vB .event = env.event.map tB)
    {e : Ev} {l : String} (hl : foldS sem l = foldS sem (tA e)) :
    eqV sem (refV vB .event) (.str (tB e)) = eqV sem (refV vA .event) (.str l) := by
  cases hev : env.event with
  | none =>
    have ha' : vA .event = none := by rw [ha, hev]; rfl
    have hb' : vB .event = none := by rw [hb, hev]; rfl
    have hl' : foldS sem l ≠ "" := by rw [hl]; exact fold_ne hA sem e
    have hl2 : foldS sem (tB e) ≠ "" := fold_ne hB sem e
    simp [refV, ha', hb', eqV_null sem hl', eqV_null sem hl2]
  | some e0 =>
    have ha' : vA .event = some (tA e0) := by rw [ha, hev]; rfl
    have hb' : vB .event = some (tB e0) := by rw [hb, hev]; rfl
    have h1 := fold_beq hA sem e0 e
    have h2 := fold_beq hB sem e0 e
    rw [← hl] at h1
    simp [refV, ha', hb', eqV_str, h1, h2]

/-- Mapping an event literal between two tables keeps `event == literal`, on every event, for either class. -/
theorem event_eq_rename (sem : Sem) {tA tB : Ev → String} (hA : EvTab tA) (hB : EvTab tB) (env : Env)
    {vA vB : View} (ha : vA .event = env.event.map tA) (hb : vB .event = env.event.map tB)
    {e : Ev} {l : String} (hl : foldS sem l = foldS sem (tA e)) :
    holds sem vB (.eq (.ref .event) (.str (tB e))) = holds sem vA (.eq (.ref .event) (.str l)) := by
  simp [holds, eval, truthy, event_cmp sem hA hB env ha hb hl]

/-- The same for `event != literal`. -/
theorem event_ne_rename (sem : Sem) {tA tB : Ev → String} (hA : EvTab tA) (hB : EvTab tB) (env : Env)
    {vA vB : View} (ha : vA .event = env.event.map tA) (hb : vB .event = env.event.map tB)
    {e : Ev} {l : String} (hl : foldS sem l = foldS sem (tA e)) :
    holds sem vB (.ne (.ref .event) (.str (tB e))) = holds sem vA (.ne (.ref .event) (.str l)) := by
  simp [holds, eval, truthy, event_cmp sem hA hB env ha hb hl]

/-- GitLab `event =~ /^literal$/i` (and `!~`) keeps its result when the literal maps between tables. -/
theorem event_rx_rename {tA tB : Ev → String} (hA : EvTab tA) (hB : EvTab tB) (env : Env)
    {vA vB : View} (ha : vA .event = env.event.map tA) (hb : vB .event = env.event.map tB)
    {e : Ev} {s : String} (hs : lower s = lower (tA e)) :
    holds .gitlab vB (.rx (.ref .event) .exact (tB e)) = holds .gitlab vA (.rx (.ref .event) .exact s) ∧
    holds .gitlab vB (.nrx (.ref .event) .exact (tB e)) = holds .gitlab vA (.nrx (.ref .event) .exact s) := by
  have key : matchV .exact (tB e) (refV vB .event) = matchV .exact s (refV vA .event) := by
    cases hev : env.event with
    | none =>
      have ha' : vA .event = none := by rw [ha, hev]; rfl
      have hb' : vB .event = none := by rw [hb, hev]; rfl
      simp [refV, ha', hb', matchV]
    | some e0 =>
      have ha' : vA .event = some (tA e0) := by rw [ha, hev]; rfl
      have hb' : vB .event = some (tB e0) := by rw [hb, hev]; rfl
      have h1 := fold_beq hA .gha e0 e
      have h2 := fold_beq hB .gha e0 e
      simp only [foldS] at h1 h2
      rw [← hs] at h1
      simp [refV, ha', hb', matchV, modeTest, h1, h2]
  constructor <;> simp [holds, eval, truthy, key]

end TranslateClass
end Pipemaster
