import Pipemaster.Core
import Pipemaster.Expr
import Pipemaster.Translate
import Pipemaster.TranslateClass

namespace Pipemaster
namespace Woodpecker

section ExprSubset

open Pipemaster.Expr Pipemaster.Translate

/-! Woodpecker `evaluate` is expr-lang over a `map[string]string` environment (constraint.go, v3.20.1).
A name the map lacks reads as "" there, not as null; the GitLab class reads an unset variable as null
and never equates null with "". The two rules disagree, so Woodpecker is not in the GitLab class
(whole-class claim refused). The model below covers the part that decides this: == between strings and
null. `GlSub` is the subset the translator uses (matches and equality tests of a ref against a non-empty
literal under !, && and ||), where the two readings agree. -/

/-- Mirrors woodpecker.UnsetEnv (expr/woodpecker/vocab.go): a ref the pipeline does not set reads as "". -/
def wpView (v : View) : View := fun r => some ((v r).getD "")

/-- Where the classes agree: a ref the pipeline sets. -/
theorem wpView_set {v : View} {r : Ref} {s : String} (h : v r = some s) : wpView v r = v r := by
  simp [wpView, h]

/-- The environment of a branch run: canonical tag is unset, as CI_COMMIT_TAG is for GitLab. -/
def branchEnv : Env := ⟨some "main", some .branch, some .push, some "abc", none⟩

/-- `tag == ""` holds in Woodpecker and fails in the GitLab class on the same run. -/
theorem eq_empty_differs :
    eval .gitlab (wpView (mapEnv branchEnv)) (.eq (.ref .tag) (.str "")) = .bool true ∧
    eval .gitlab (mapEnv branchEnv) (.eq (.ref .tag) (.str "")) = .bool false := by
  decide +kernel

/-- `tag == null` fails in Woodpecker and holds in the GitLab class on the same run. -/
theorem eq_null_differs :
    eval .gitlab (wpView (mapEnv branchEnv)) (.eq (.ref .tag) .null) = .bool false ∧
    eval .gitlab (mapEnv branchEnv) (.eq (.ref .tag) .null) = .bool true := by
  decide +kernel

/-- No translation from the GitLab class to Woodpecker, or back, can be exact for every expression:
some expression evaluates differently under the two readings of the same run. -/
theorem not_gitlab_class :
    ¬ ∀ e : E, eval .gitlab (wpView (mapEnv branchEnv)) e = eval .gitlab (mapEnv branchEnv) e := by
  intro h
  have := h (.eq (.ref .tag) (.str ""))
  have h2 := eq_empty_differs
  rw [this] at h2
  have := h2.1.symm.trans h2.2
  cases this

/-! ### The subset where Woodpecker agrees with the GitLab class

`GlSub` holds for regex and equality tests of a ref against a non-empty literal, combined with `!`, `&&` and
`||`. Every node is boolean-valued, so expr-lang type-checks them, and a ref is read only inside a test. -/

/-- Mirrors woodpecker.inSubset (expr/woodpecker/subset.go). -/
def GlSub : E → Bool
  | .rx (.ref _) _ s | .nrx (.ref _) _ s => s != ""
  | .eq (.ref _) (.str s) | .ne (.ref _) (.str s) => s != ""
  | .not x => GlSub x
  | .and l r | .or l r => GlSub l && GlSub r
  | _ => false

theorem lower_ne_empty {s : String} (h : s ≠ "") : lower s ≠ "" := by
  intro hl
  apply h
  have : (s.toList.map Char.toLower) = [] := by
    have := congrArg String.toList hl
    simpa [lower] using this
  have h2 : s.toList = [] := List.map_eq_nil_iff.mp this
  exact String.toList_eq_nil_iff.mp h2

/-- A non-empty literal never matches the empty string, in any mode. -/
theorem modeTest_empty (m : Mode) {s : String} (h : s ≠ "") : modeTest m "" (lower s) = false := by
  have hl := lower_ne_empty h
  obtain ⟨a, t, ht⟩ : ∃ a t, (lower s).toList = a :: t := by
    cases hx : (lower s).toList with
    | nil => exact absurd (String.toList_eq_nil_iff.mp hx) hl
    | cons a t => exact ⟨a, t, rfl⟩
  have hne : ("" == lower s) = false := by
    simp only [beq_eq_false_iff_ne, ne_eq]
    exact fun he => hl he.symm
  cases m <;> simp [modeTest, isInfix, ht, hne]

/-- A regex test of a non-empty literal reads an unset ref ("" in Woodpecker) as it reads null. -/
theorem matchV_unset (m : Mode) {s : String} (h : s ≠ "") (vA vB : View) (r : Ref)
    (hr : vB r = some ((vA r).getD "")) : matchV m s (refV vB r) = matchV m s (refV vA r) := by
  cases hA : vA r with
  | none => simp [refV, hr, hA, matchV, lower_empty, modeTest_empty m h]
  | some x => simp [refV, hr, hA]

/-- A string equality against a non-empty literal reads an unset ref ("" in Woodpecker) as it reads null. -/
theorem eqV_unset {s : String} (h : s ≠ "") (vA vB : View) (r : Ref)
    (hr : vB r = some ((vA r).getD "")) :
    eqV .gitlab (refV vB r) (.str s) = eqV .gitlab (refV vA r) (.str s) := by
  cases hA : vA r with
  | none =>
    have hne : ("" == s) = false := by simpa [beq_eq_false_iff_ne] using Ne.symm h
    simp [refV, hr, hA, eqV, hne]
  | some x => simp [refV, hr, hA]

/-- On `GlSub` expressions, Woodpecker evaluation (an unset ref reads "") equals GitLab-class evaluation. -/
theorem eval_woodpecker (vA vB : View) :
    ∀ e : E, GlSub e = true → (∀ r ∈ TranslateClass.refs e, vB r = some ((vA r).getD "")) →
      eval .gitlab vB e = eval .gitlab vA e := by
  intro e
  induction e with
  | rx x m s =>
    intro h hr
    cases x with
    | ref r =>
      have hs : s ≠ "" := by simpa [GlSub] using h
      have hr' := hr r (by simp [TranslateClass.refs])
      simp [eval, matchV_unset m hs vA vB r hr']
    | _ => simp [GlSub] at h
  | nrx x m s =>
    intro h hr
    cases x with
    | ref r =>
      have hs : s ≠ "" := by simpa [GlSub] using h
      have hr' := hr r (by simp [TranslateClass.refs])
      simp [eval, matchV_unset m hs vA vB r hr']
    | _ => simp [GlSub] at h
  | eq l r _ _ =>
    intro h hr
    cases l with
    | ref x =>
      cases r with
      | str s =>
        have hs : s ≠ "" := by simpa [GlSub] using h
        have hr' := hr x (by simp [TranslateClass.refs])
        simp [eval, eqV_unset hs vA vB x hr']
      | _ => simp [GlSub] at h
    | _ => simp [GlSub] at h
  | ne l r _ _ =>
    intro h hr
    cases l with
    | ref x =>
      cases r with
      | str s =>
        have hs : s ≠ "" := by simpa [GlSub] using h
        have hr' := hr x (by simp [TranslateClass.refs])
        simp [eval, eqV_unset hs vA vB x hr']
      | _ => simp [GlSub] at h
    | _ => simp [GlSub] at h
  | not x ih =>
    intro h hr
    simp only [GlSub] at h
    simp only [eval]
    rw [ih h (by simpa [TranslateClass.refs] using hr)]
  | and l r ihl ihr =>
    intro h hr
    simp only [GlSub, Bool.and_eq_true] at h
    simp only [TranslateClass.refs, List.mem_append] at hr
    simp only [eval]
    rw [ihl h.1 (fun x hx => hr x (Or.inl hx)), ihr h.2 (fun x hx => hr x (Or.inr hx))]
  | or l r ihl ihr =>
    intro h hr
    simp only [GlSub, Bool.and_eq_true] at h
    simp only [TranslateClass.refs, List.mem_append] at hr
    simp only [eval]
    rw [ihl h.1 (fun x hx => hr x (Or.inl hx)), ihr h.2 (fun x hx => hr x (Or.inr hx))]
  | _ => intro h; simp [GlSub] at h

/-- The truthiness form: a `GlSub` expression holds in Woodpecker exactly when it holds in the GitLab class. -/
theorem woodpecker_sound (vA vB : View) {e : E} (h : GlSub e = true)
    (hr : ∀ r ∈ TranslateClass.refs e, vB r = some ((vA r).getD "")) :
    holds .gitlab vB e = holds .gitlab vA e := by
  unfold holds; rw [eval_woodpecker vA vB e h hr]

/-- A bare ref as the whole condition holds in the GitLab class exactly when `ref != ""` holds in Woodpecker,
where an unset ref reads "". The emitter spells the bare ref that way (woodpecker.Boolean, expr/woodpecker/when.go). -/
theorem woodpecker_bare (vA vB : View) (r : Ref) (hr : vB r = some ((vA r).getD "")) :
    holds .gitlab vB (.ne (.ref r) (.str "")) = holds .gitlab vA (.ref r) := by
  cases hA : vA r with
  | none => simp [holds, eval, truthy, refV, eqV, hr, hA]
  | some x => simp [holds, eval, truthy, refV, eqV, hr, hA, bne]

/-- An event test `event =~ /^literal$/i` keeps its result when the literal moves from the GitLab-class
event table `tA` (unset is null) to the Woodpecker table `tB` (unset is ""). -/
theorem woodpecker_event_rx {tA tB : Ev → String} (hA : TranslateClass.EvTab tA) (hB : TranslateClass.EvTab tB)
    (env : Env) {vA vB : View} (ha : vA .event = env.event.map tA)
    (hb : vB .event = some ((env.event.map tB).getD ""))
    {e : Ev} {s : String} (hs : lower s = lower (tA e)) :
    holds .gitlab vB (.rx (.ref .event) .exact (tB e)) = holds .gitlab vA (.rx (.ref .event) .exact s) ∧
    holds .gitlab vB (.nrx (.ref .event) .exact (tB e)) = holds .gitlab vA (.nrx (.ref .event) .exact s) := by
  have key : matchV .exact (tB e) (refV vB .event) = matchV .exact s (refV vA .event) := by
    cases hev : env.event with
    | none =>
      have ha' : vA .event = none := by rw [ha, hev]; rfl
      have hb' : vB .event = some "" := by rw [hb, hev]; rfl
      have hne : lower (tB e) ≠ "" := hB.ne e
      simp [refV, ha', hb', matchV, modeTest, lower_empty, Ne.symm hne]
    | some e0 =>
      have ha' : vA .event = some (tA e0) := by rw [ha, hev]; rfl
      have hb' : vB .event = some (tB e0) := by rw [hb, hev]; rfl
      have h1 := TranslateClass.fold_beq hA .gha e0 e
      have h2 := TranslateClass.fold_beq hB .gha e0 e
      simp only [TranslateClass.foldS] at h1 h2
      rw [← hs] at h1
      simp [refV, ha', hb', matchV, modeTest, h1, h2]
  constructor <;> simp [holds, eval, truthy, key]

/-- A GitLab-class event comparison `event == v` (or `!=`), where `v` is exactly the value the source table `tA` gives
event `e`, keeps its result as the Woodpecker pattern test `event =~ /^w$/i` (or `!~`) with `w` the value the
Woodpecker table `tB` gives `e`: the unset event is null in the source and "" in Woodpecker, and the tables are
injective after lowering. This is the rewrite the translator makes for a target that reads no event equality
(Mirrors expr.mapEventBinary (expr/lang.go)). -/
theorem woodpecker_event_eq {tA tB : Ev → String} (hA : TranslateClass.EvTab tA) (hB : TranslateClass.EvTab tB)
    (env : Env) {vA vB : View} (ha : vA .event = env.event.map tA)
    (hb : vB .event = some ((env.event.map tB).getD ""))
    {e : Ev} {l : String} (hl : l = tA e) :
    holds .gitlab vB (.rx (.ref .event) .exact (tB e)) = holds .gitlab vA (.eq (.ref .event) (.str l)) ∧
    holds .gitlab vB (.nrx (.ref .event) .exact (tB e)) = holds .gitlab vA (.ne (.ref .event) (.str l)) := by
  subst hl
  have key : matchV .exact (tB e) (refV vB .event) = eqV .gitlab (refV vA .event) (.str (tA e)) := by
    cases hev : env.event with
    | none =>
      have ha' : vA .event = none := by rw [ha, hev]; rfl
      have hb' : vB .event = some "" := by rw [hb, hev]; rfl
      have hne : lower (tB e) ≠ "" := hB.ne e
      have hA' : TranslateClass.foldS .gitlab (tA e) ≠ "" := TranslateClass.fold_ne hA .gitlab e
      simp [refV, ha', hb', matchV, modeTest, lower_empty, Ne.symm hne, TranslateClass.eqV_null .gitlab hA']
    | some e0 =>
      have ha' : vA .event = some (tA e0) := by rw [ha, hev]; rfl
      have hb' : vB .event = some (tB e0) := by rw [hb, hev]; rfl
      have h1 := TranslateClass.fold_beq hA .gitlab e0 e
      have h2 := TranslateClass.fold_beq hB .gha e0 e
      simp only [TranslateClass.foldS] at h1 h2
      simp [refV, ha', hb', matchV, modeTest, eqV, h1, h2]
  constructor <;> simp [holds, eval, truthy, key]

/-! ### A complement as a list of events

Woodpecker has events the source never reports (`pull_request_closed`, `release`, `deployment`, ...), so a
complement `event != v` cannot be `event !~ /^v$/i`: that passes them all. The translator (expr.eventListsRx) writes
the other events the source reports, each as its Woodpecker spelling, joined by `||`. An event with no spelling drops
out, which only narrows the list. The theorems quantify over every Woodpecker event string `w`, so the events with no
source twin are covered, not assumed away. -/

/-- The Woodpecker events of the complement (Mirrors expr.eventListExcept (expr/event_rx.go)): one exact pattern per spelling. -/
def anyOf : List String → E
  | [] => .bool false
  | x :: xs => .or (.rx (.ref .event) .exact x) (anyOf xs)

/-- Every event the model names. -/
def evAll : List Ev := [.push, .pullRequest, .schedule, .workflowDispatch]

theorem mem_evAll (e : Ev) : e ∈ evAll := by cases e <;> simp [evAll]

/-- The list holds exactly when the event equals one of its spellings, ignoring case. -/
theorem anyOf_holds {vB : View} {w : String} (hb : vB .event = some w) (ws : List String) :
    holds .gitlab vB (anyOf ws) = true ↔ ∃ x ∈ ws, lower w = lower x := by
  induction ws with
  | nil => simp [anyOf, holds, eval, truthy]
  | cons x xs ih =>
    have hx : matchV .exact x (refV vB .event) = (lower w == lower x) := by
      simp [refV, hb, matchV, modeTest]
    by_cases h : (lower w == lower x) = true
    · have h' : lower w = lower x := by simpa using h
      simp [anyOf, holds, eval, truthy, logicalTruthy, rubyTruthy, hx, h, h']
    · have h' : ¬ lower w = lower x := by simpa using h
      have h2 : (lower w == lower x) = false := by simpa using h
      simp only [anyOf, holds, eval, truthy, logicalTruthy, rubyTruthy, hx, h2, Bool.false_eq_true, ite_false] at ih ⊢
      simpa [h'] using ih

/-- An event that no spelling of the list names never passes it: the target-only events are out. -/
theorem woodpecker_event_ne_target_only {vB : View} {w : String} (hb : vB .event = some w) (ws : List String)
    (h : ∀ x ∈ ws, lower w ≠ lower x) : holds .gitlab vB (anyOf ws) = false := by
  cases hh : holds .gitlab vB (anyOf ws)
  · rfl
  · obtain ⟨x, hx, hxe⟩ := (anyOf_holds hb ws).1 hh
    exact absurd hxe (h x hx)

/-- On a run whose Woodpecker event `w` is a spelling of the source event `ev`, the list of the other events'
spellings holds exactly when the source test `event != tA e0` does, and its negation exactly when `event == tA e0`
does. `sp` gives the spellings of an event; no spelling serves two events. -/
theorem woodpecker_event_ne_list {tA : Ev → String} (hA : TranslateClass.EvTab tA) (sp : Ev → List String)
    (hsp : ∀ a b x y, x ∈ sp a → y ∈ sp b → lower x = lower y → a = b) (e0 : Ev)
    {vA vB : View} {ev : Ev} (ha : vA .event = some (tA ev)) {w : String} (hb : vB .event = some w)
    (hw : w ∈ sp ev) :
    holds .gitlab vB (anyOf ((evAll.filter (· ≠ e0)).flatMap sp)) =
        holds .gitlab vA (.ne (.ref .event) (.str (tA e0))) ∧
    holds .gitlab vB (.not (anyOf ((evAll.filter (· ≠ e0)).flatMap sp))) =
        holds .gitlab vA (.eq (.ref .event) (.str (tA e0))) := by
  have key : holds .gitlab vB (anyOf ((evAll.filter (· ≠ e0)).flatMap sp)) = true ↔ ev ≠ e0 := by
    rw [anyOf_holds hb]
    constructor
    · rintro ⟨x, hx, hxe⟩ rfl
      simp only [List.mem_flatMap, List.mem_filter, decide_eq_true_eq] at hx
      obtain ⟨b, ⟨_, hb0⟩, hxb⟩ := hx
      exact hb0 (hsp _ _ _ _ hw hxb hxe).symm
    · intro hne
      exact ⟨w, by
        simp only [List.mem_flatMap, List.mem_filter, decide_eq_true_eq]
        exact ⟨ev, ⟨mem_evAll ev, hne⟩, hw⟩, rfl⟩
  have src : eqV .gitlab (refV vA .event) (.str (tA e0)) = true ↔ ev = e0 := by
    simp only [refV, ha, eqV, beq_iff_eq]
    constructor
    · intro h; exact hA.inj _ _ (by rw [h])
    · rintro rfl; rfl
  have hL : holds .gitlab vB (anyOf ((evAll.filter (· ≠ e0)).flatMap sp)) = decide (ev ≠ e0) := by
    rw [Bool.eq_iff_iff]; simpa using key
  have hE : eqV .gitlab (refV vA .event) (.str (tA e0)) = decide (ev = e0) := by
    rw [Bool.eq_iff_iff]; simpa using src
  have hnot : ∀ l, holds .gitlab vB (.not l) = !holds .gitlab vB l := fun l => rfl
  have hne : ∀ l r, holds .gitlab vA (.ne l r) = !eqV .gitlab (eval .gitlab vA l) (eval .gitlab vA r) :=
    fun l r => rfl
  have heq : ∀ l r, holds .gitlab vA (.eq l r) = eqV .gitlab (eval .gitlab vA l) (eval .gitlab vA r) :=
    fun l r => rfl
  constructor
  · rw [hL, hne]; simp only [eval, hE]; simp
  · rw [hnot, hL, heq]; simp only [eval, hE]; simp

end ExprSubset

/-! ## The adapter on the core subset

`WFile` mirrors the keys the emitter writes for one workflow file (provider/woodpecker), not YAML syntax. Each
marker of the file is a field of the file or of the step it names by position, because the parser reads them by
path and position (collapse.go). Three string codecs are not in the model and the Go vector test checks them on
the vector alphabet only: the `$` to `$$` escape of command, variable and marker text, the `refs/tags/` prefix
of a tag filter, and the YAML quoting of every scalar. The model holds the literal strings.

Not in the model, each on purpose:
- A pipeline with no job: the emitter writes a placeholder workflow named `empty`, which the parser drops.
- A step with no script line: the emitter does not write it and keeps it in a `step.action` marker.
- Expressions, matrices, actions, services, containers, caches, artifacts, outputs, environments, concurrency:
  the same subset as the other adapters (`Core`).
- A glob that doublestar reads differently from the IR (`exactPat`): the emitter writes a pipeline-level
  marker for it, and a pattern with `+` disables the workflow with an `evaluate: "false"` entry.
- De-duplication of equal triggers (parse_when.go `triggers`): the emitter never writes two equal entries. -/

section Adapter

open Core

/-- One `when` entry. `events` is a list; `ref` holds the tag globs without the `refs/tags/` prefix. -/
structure Entry where
  events : List String
  branch : List String
  ref : List String
  status : List String
  deriving DecidableEq, Repr

/-- One `depends_on` item. -/
structure Dep where
  name : String
  optional : Bool
  deriving DecidableEq, Repr

/-- One written step. `env` is the written `environment` (own variables, then inherited ones), `shadow` the names
of the `step.env` marker, `ignore` the `failure: ignore` key. -/
structure WStep where
  name : String
  cmds : List String
  env : List Var
  ignore : Bool
  shadow : List String
  deriving DecidableEq, Repr

/-- A trigger as the `job.manual` marker carries it: kind (`pr`, `push`, `tag`) and include list. -/
structure Trig where
  kind : String
  incl : List String
  deriving DecidableEq, Repr

/-- A workflow file; `name` is the file stem. `jobEnv` is the `job.env` marker (empty when absent), `noop` the
`step.script` marker of the job, `manual` the `job.manual` marker with its triggers, `fin` the `pipeline.finally`
marker (needs and `noIf`), `tagIdx` the `trigger.filter.tags` marker, `platform` the `labels.platform` key, `tags`
the other `labels` keys, each with the value `true`. -/
structure WFile where
  name : String
  whens : List Entry
  deps : List Dep
  steps : List WStep
  noop : Bool
  jobEnv : List Var
  allowFailure : Bool
  manual : Option (List Trig)
  fin : Option (List Core.Need × Bool)
  tagIdx : Option Nat
  platform : Option String
  tags : List String
  deriving DecidableEq, Repr

/-! ### Constants of the emitter and parser -/

/-- Mirrors woodpecker.pipelineID (provider/woodpecker/dialect.go): the ID Parse gives its one pipeline. -/
def wpPipelineId : String := "woodpecker"
/-- Mirrors woodpecker.placeholderName (provider/woodpecker/emit_step.go). -/
def noopName : String := "noop"
/-- The command of the placeholder step (emit_step.go placeholder). -/
def noopCmd : String := "true"
def kindPr : String := "pr"
def kindPush : String := "push"
def kindTag : String := "tag"
/-- Mirrors woodpecker.eventPush (provider/woodpecker/parse_when.go). -/
def evPush : String := "push"
/-- Mirrors woodpecker.eventPR (provider/woodpecker/parse_when.go). -/
def evPr : String := "pull_request"
/-- Mirrors woodpecker.eventTag (provider/woodpecker/parse_when.go). -/
def evTag : String := "tag"
/-- Mirrors woodpecker.eventMan (provider/woodpecker/parse_when.go). -/
def evManual : String := "manual"
/-- Mirrors woodpecker.statusOK (provider/woodpecker/emit_when.go). -/
def statusOk : String := "success"
/-- Mirrors woodpecker.statusFail (provider/woodpecker/emit_when.go). -/
def statusFail : String := "failure"
/-- Mirrors woodpecker.allEvents (provider/woodpecker/parse_when.go): `completeEvents` gives an entry without an
event filter every event of the schema. -/
def allEvents : List String :=
  ["push", "pull_request", "pull_request_closed", "pull_request_metadata", "tag", "release", "deployment",
   "cron", "manual"]

/-! ### Emission -/

/-- A character of `platformRE`'s classes `[a-z0-9]` (provider/woodpecker/emit_job.go:35). -/
def platChar (c : Char) : Bool := c.isLower || c.isDigit

def platPart (s : String) : Bool := !s.isEmpty && s.toList.all platChar

/-- Mirrors woodpecker.platformRE (provider/woodpecker/emit_job.go): `^[a-z0-9]+/[a-z0-9]+(/v[0-9]+)?$`. -/
def platformLabel (l : String) : Bool :=
  match l.splitOn "/" with
  | [a, b] => platPart a && platPart b
  | [a, b, v] =>
    platPart a && platPart b &&
      (match v.toList with | 'v' :: d :: ds => (d :: ds).all Char.isDigit | _ => false)
  | _ => false

/-- Mirrors woodpecker.emitter.labels (provider/woodpecker/emit_job.go): the first runner label of the
`os/arch` form becomes `labels.platform`; every other label is a tag (see `tagsOf`). -/
def platformOf (r : List String) : Option String := r.find? platformLabel

/-- A character of the label-name class `[A-Za-z0-9._-]` (emitkit.tagRE, provider/emitkit/runner.go). -/
def tagChar (c : Char) : Bool := c.isAlphanum || c == '.' || c == '_' || c == '-'

/-- Mirrors emitkit.TagKey (provider/emitkit/runner.go): at most 63 characters, name characters only, and an
alphanumeric first and last character. -/
def tagKeyOk (l : String) : Bool :=
  l.length ≤ 63 && l.toList.all tagChar && (l.toList.head?.map Char.isAlphanum).getD false &&
    (l.toList.getLast?.map Char.isAlphanum).getD false

/-- The runner labels that become `labels` keys beside the platform: a valid label name that is not the platform
label and not the key `platform`. Any other label is lost. -/
def tagsOf (r : List String) : List String :=
  r.filter fun l => some l != platformOf r && tagKeyOk l && l != "platform"

/-- The pipeline triggers in IR order (`ir.CorePipeline.Project`): pr, push, tag. -/
def trigsOf (p : Pipe) : List Trig :=
  (p.pr.map fun l => (⟨kindPr, l⟩ : Trig)).toList ++ (p.push.map fun l => (⟨kindPush, l⟩ : Trig)).toList ++
  (p.tag.map fun l => (⟨kindTag, l⟩ : Trig)).toList

/-- Mirrors woodpecker.emitter.triggerEntry (provider/woodpecker/emit_when.go). -/
def trigEntry (t : Trig) : Entry :=
  if t.kind = kindTag then ⟨[evTag], [], t.incl, []⟩
  else ⟨[if t.kind = kindPr then evPr else evPush], t.incl, [], []⟩

/-- A pipeline with no trigger writes one entry with every event (`completeEvents`). -/
def baseEntries (p : Pipe) : List Entry :=
  if trigsOf p = [] then [⟨allEvents, [], [], []⟩] else (trigsOf p).map trigEntry

def manualEntries : List Entry := [⟨[evManual], [], [], []⟩]

/-- A finally workflow runs on the status of its job: `failure` alone, or success and failure. -/
def finStatus (j : Core.Job) : List String := if j.onFailure then [statusFail] else [statusOk, statusFail]

/-- The position of the tag trigger in `Pipeline.Triggers`, which the `trigger.filter.tags` marker names. -/
def tagIdxOf (p : Pipe) : Option Nat :=
  match p.tag with
  | some (_ :: _) => some ((if p.pr.isSome then 1 else 0) + (if p.push.isSome then 1 else 0))
  | _ => none

/-- Variables of the job that the step does not set itself: the emitter copies them into the step. -/
def inherited (j : Core.Job) (s : Core.Step) : List Var :=
  j.env.filter fun v => !(s.env.map (·.name)).contains v.name

/-- Mirrors woodpecker.emitter.markShadows (provider/woodpecker/markers.go): the own variables that repeat an
inherited variable with the same value. -/
def shadowOf (j : Core.Job) (s : Core.Step) : List String :=
  (s.env.filter fun v => j.env.contains v).map (·.name)

def wstep (j : Core.Job) (s : Core.Step) : WStep :=
  ⟨s.id, s.script, s.env ++ inherited j s, j.allowFailure, shadowOf j s⟩

/-- The step that stands in for a job with no step (emit_step.go placeholder). -/
def noopStep : WStep := ⟨noopName, [noopCmd], [], false, []⟩

def stepsOf (j : Core.Job) : List WStep := if j.steps.isEmpty then [noopStep] else j.steps.map (wstep j)

/-- A main job: its needs are required dependencies, and a manual job replaces its events with `manual`. -/
def mainFile (p : Pipe) (j : Core.Job) : WFile where
  name := j.id
  whens := if j.manual then manualEntries else baseEntries p
  deps := j.needs.map fun n => ⟨n.job, false⟩
  steps := stepsOf j
  noop := j.steps.isEmpty
  jobEnv := j.env
  allowFailure := j.allowFailure
  manual := if j.manual then some (trigsOf p) else none
  fin := none
  tagIdx := tagIdxOf p
  platform := platformOf j.runner
  tags := tagsOf j.runner

/-- Mirrors woodpecker.emitter.dependsOn (provider/woodpecker/emit_job.go): a finally workflow waits for every
main workflow as optional. Its needs are on main jobs, so they add nothing, and it keeps them in the marker. -/
def finFile (p : Pipe) (j : Core.Job) : WFile where
  name := j.id
  whens := (baseEntries p).map fun e => { e with status := finStatus j }
  deps := p.jobs.map fun m => ⟨m.id, true⟩
  steps := stepsOf j
  noop := j.steps.isEmpty
  jobEnv := j.env
  allowFailure := j.allowFailure
  manual := none
  fin := some (j.needs, !j.onFailure)
  tagIdx := tagIdxOf p
  platform := platformOf j.runner
  tags := tagsOf j.runner

/-- Mirrors woodpecker.Emit (provider/woodpecker/emit.go) on the core subset: one file per job, main jobs first. -/
def toWp (p : Pipe) : List WFile := p.jobs.map (mainFile p) ++ p.fin.map (finFile p)

/-! ### Parsing -/

/-- A pattern doublestar reads as the IR does (parse_when.go exactGlob). The model is stricter: it also refuses
`*` and the empty pattern. -/
def ExactPat (s : String) : Prop :=
  s.toList ≠ [] ∧ ∀ c ∈ s.toList, c ∉ ['!', '+', '?', '[', ']', '{', '}', '\\', '*']

instance (s : String) : Decidable (ExactPat s) := by unfold ExactPat; exact inferInstance

/-- One entry read as a trigger (parse_when.go `triggers`): a single push, pull request or tag event with exact
filters and no status. -/
def entryTrig (e : Entry) : Option Trig :=
  if e.status = [] ∧ e.events = [evPush] ∧ e.ref = [] ∧ decide (∀ x ∈ e.branch, ExactPat x) then
    some ⟨kindPush, e.branch⟩
  else if e.status = [] ∧ e.events = [evPr] ∧ e.ref = [] ∧ decide (∀ x ∈ e.branch, ExactPat x) then
    some ⟨kindPr, e.branch⟩
  else if e.status = [] ∧ e.events = [evTag] ∧ decide (∀ x ∈ e.ref, ExactPat x) then
    some ⟨kindTag, e.ref⟩
  else none

/-- A finally file ignores the status when it reads triggers (`whenSpec.view`, `ignoreStatus`). -/
def WFile.view (f : WFile) : List Entry :=
  if f.fin.isSome then f.whens.map fun e => { e with status := [] } else f.whens

def whenTrigs (f : WFile) : Option (List Trig) := f.view.mapM entryTrig

/-- The trigger list that every workflow in `sel` agrees on (parse.go `sharedTriggers`). -/
def sharedTrigs (sel : List WFile) : Option (List Trig) :=
  match sel.map whenTrigs with
  | [] => none
  | x :: r => if r.all (fun y => decide (y = x)) then x else none

/-- Mirrors woodpecker.collapseTriggers (provider/woodpecker/collapse.go): the triggers the non-manual workflows
share, replaced by the list of a manual workflow's marker. -/
def readTriggers (fs : List WFile) : List Trig :=
  let sel := fs.filter fun f => f.manual.isNone
  if !sel.isEmpty && (sharedTrigs sel).isNone then []
  else match fs.findSome? (·.manual) with
    | some orig => orig
    | none => (sharedTrigs sel).getD []

def trigFind (k : String) (ts : List Trig) : Option (List String) :=
  ts.findSome? fun t => if t.kind = k then some t.incl else none

/-- Mirrors woodpecker.dropCopies (provider/woodpecker/collapse.go): drop the copies of the job variables, except
the variables the step marker names. -/
def parseStep (jobEnv : List Var) (s : WStep) : Core.Step :=
  ⟨s.name, "", s.cmds, s.env.filter fun v => s.shadow.contains v.name || !jobEnv.contains v⟩

/-- Mirrors woodpecker.collapseNoop (provider/woodpecker/collapse.go). -/
def parseSteps (f : WFile) : List Core.Step :=
  if f.noop && f.steps.map (·.name) == [noopName] then [] else f.steps.map (parseStep f.jobEnv)

/-- A finally job is `onFailure` when its status gate is `failure` alone (`whenSpec.residual`). -/
def parseFile (f : WFile) : Core.Job where
  id := f.name
  name := ""
  needs := match f.fin with
    | some (ns, _) => ns
    | none => f.deps.map fun d => ⟨d.name, false⟩
  env := f.jobEnv
  runner := f.platform.toList
  steps := parseSteps f
  retry := 0
  timeout := 0
  manual := f.manual.isSome
  allowFailure := f.allowFailure
  onFailure := f.fin.isSome && decide ((f.whens.head?.map (·.status)) = some [statusFail])

/-- The pipeline Parse builds before the round-trip metadata is applied. -/
def rawWp (fs : List WFile) : Pipe :=
  { id := wpPipelineId
    name := ""
    push := trigFind kindPush (readTriggers fs)
    tag := trigFind kindTag (readTriggers fs)
    pr := trigFind kindPr (readTriggers fs)
    jobs := (fs.filter fun f => f.fin.isNone).map parseFile
    fin := (fs.filter fun f => f.fin.isSome).map parseFile }

/-- The metadata layer of the Woodpecker adapter: the core layer, the pipeline rename to `woodpecker`
(roundtrip.IDsFor), and the runner labels, retry runs and timeout of each job, which the emitter reports lost or
external (caps.JobRunner, caps.JobRetry, caps.JobTimeout; meta.CollectLost keeps them in `Lost.Job`). -/
def wpMeta (p : Pipe) : Meta :=
  { metaOf p with
    ids := fun q => if q = wpPipelineId ∧ p.id ≠ wpPipelineId then some p.id else none
    runner := fun
      | .job pid jid =>
        if pid = p.id then (findJob p jid).bind fun j => if j.runner = [] then none else some j.runner else none
      | _ => none
    retry := fun
      | .job pid jid => if pid = p.id then (findJob p jid).bind fun j => if j.retry = 0 then none else some j.retry else none
      | _ => none
    timeout := fun
      | .job pid jid =>
        if pid = p.id then (findJob p jid).bind fun j => if j.timeout = 0 then none else some j.timeout else none
      | _ => none }

/-- Mirrors woodpecker.Parse (provider/woodpecker/parse.go) on the core subset; the round-trip metadata `m` is
applied last (roundtrip.Restore). -/
def fromWp (fs : List WFile) (m : Meta) : Pipe := restore (rawWp fs) m

/-! ### The fragment -/

/-- A text with the start of an expression template, which the emitter reads as an expression: a lone `${{`
makes the emit fail. -/
def tplOpen : List Char → Bool
  | '$' :: '{' :: '{' :: _ => true
  | _ :: t => tplOpen t
  | [] => false

/-- A text that names the file of a Tekton result (`$(results.`). Outside Tekton the text is a command substitution
that fails, so the emitter points the result file at /dev/null and reports job.outputs lost (emitkit result.go
NeutralizeResultWrite). The model
refuses the whole prefix, which is stricter than the matcher. -/
def resultOpen : List Char → Bool
  | '$' :: '(' :: 'r' :: 'e' :: 's' :: 'u' :: 'l' :: 't' :: 's' :: '.' :: _ => true
  | _ :: t => resultOpen t
  | [] => false

/-- A script line the model keeps. A newline joins into one multi-line command that the parser splits again
(emit_step.go commandTexts, parse_step.go:127). An unterminated `${{` is an emit error (expr/template.go:37), and a closed
one moves the step into a `step.action` marker. -/
def LineOk (l : String) : Prop := '\n' ∉ l.toList ∧ tplOpen l.toList = false ∧ resultOpen l.toList = false

/-- A variable value the codec keeps: a newline with a tab breaks the YAML scalar. -/
def ValueOk (v : String) : Prop := '\n' ∉ v.toList

instance (l : String) : Decidable (LineOk l) := by unfold LineOk; exact inferInstance
instance (v : String) : Decidable (ValueOk v) := by unfold ValueOk; exact inferInstance

def trigPats (p : Pipe) : List String := (p.push.getD []) ++ (p.tag.getD []) ++ (p.pr.getD [])

/-- What Woodpecker needs beyond `Wf`: the pipeline has a job; a manual job has a trigger to restore (without one
the parser reads the `manual` event as a job condition, which `Core` cannot hold); trigger patterns are exact;
every step has a script of lines the codec keeps; and variable values hold no newline. -/
def WpOk (p : Pipe) : Prop :=
  (∀ j ∈ p.all, j.manual = true → (p.push.isSome ∨ p.tag.isSome ∨ p.pr.isSome)) ∧
  (∀ x ∈ trigPats p, ExactPat x) ∧ p.all ≠ [] ∧
  (∀ j ∈ p.all, (∀ v ∈ j.env, ValueOk v.value) ∧
    ∀ s ∈ j.steps, s.script ≠ [] ∧ (∀ l ∈ s.script, LineOk l) ∧ ∀ v ∈ s.env, ValueOk v.value)

instance (p : Pipe) : Decidable (WpOk p) := by unfold WpOk; exact inferInstance

/-- The Woodpecker fragment (Mirrors woodpecker.Caps (provider/woodpecker/caps.go)): the shared structure plus
Woodpecker's own needs. -/
def WpFrag (p : Pipe) : Prop := Wf p ∧ WpOk p

instance (p : Pipe) : Decidable (WpFrag p) := by unfold WpFrag; exact inferInstance

/-! ### Theorem 2: parse inverts emit on the Woodpecker fragment -/

/-- What Woodpecker keeps of a main job before the metadata: no names, no artifacts flag on needs, no retry or
timeout, and no runner label but the platform label. -/
def stripName (s : Core.Step) : Core.Step := { s with name := "" }

def wpStripMain (j : Core.Job) : Core.Job :=
  { j with name := "", needs := j.needs.map fun n => { n with artifacts := false }, runner := (platformOf j.runner).toList,
           steps := j.steps.map stripName, retry := 0, timeout := 0 }

/-- A finally job keeps its needs with the flags, because the `pipeline.finally` marker carries them. -/
def wpStripFin (j : Core.Job) : Core.Job :=
  { j with name := "", runner := (platformOf j.runner).toList, steps := j.steps.map stripName, retry := 0, timeout := 0 }

theorem env_roundtrip (j : Core.Job) (s : Core.Step) :
    ((s.env ++ inherited j s).filter fun v => (shadowOf j s).contains v.name || !j.env.contains v) = s.env := by
  have mem_shadow : ∀ v, v ∈ s.env → v ∈ j.env → v.name ∈ shadowOf j s := by
    intro v hv hc
    simp only [shadowOf, List.mem_map, List.mem_filter]
    exact ⟨v, ⟨hv, by simpa using hc⟩, rfl⟩
  rw [List.filter_append]
  have h1 : s.env.filter (fun v => (shadowOf j s).contains v.name || !j.env.contains v) = s.env := by
    apply List.filter_eq_self.mpr
    intro v hv
    by_cases hc : v ∈ j.env
    · simp [mem_shadow v hv hc]
    · simp [hc]
  have h2 : (inherited j s).filter (fun v => (shadowOf j s).contains v.name || !j.env.contains v) = [] := by
    apply List.filter_eq_nil_iff.mpr
    intro v hv
    simp only [inherited, List.mem_filter] at hv
    obtain ⟨hvj, hvn⟩ := hv
    have hn : v.name ∉ s.env.map (·.name) := by simpa using hvn
    have hs : v.name ∉ shadowOf j s := by
      intro hcon
      simp only [shadowOf, List.mem_map, List.mem_filter] at hcon
      obtain ⟨w, ⟨hw, _⟩, hwn⟩ := hcon
      exact hn (List.mem_map.2 ⟨w, hw, hwn⟩)
    simp [hs, hvj]
  rw [h1, h2, List.append_nil]

theorem parseStep_wstep (j : Core.Job) (s : Core.Step) : parseStep j.env (wstep j s) = stripName s := by
  unfold parseStep wstep stripName
  simp only [env_roundtrip]

theorem parseSteps_main (p : Pipe) (j : Core.Job) :
    parseSteps (mainFile p j) = j.steps.map stripName := by
  unfold parseSteps mainFile stepsOf
  cases hs : j.steps with
  | nil => simp [noopStep, noopName]
  | cons a t =>
    simp only [List.isEmpty_cons, Bool.false_eq_true, ite_false, Bool.false_and, List.map_cons]
    simp only [List.map_map, Function.comp_def, parseStep_wstep] at *

theorem parseSteps_fin (p : Pipe) (j : Core.Job) :
    parseSteps (finFile p j) = j.steps.map stripName := by
  unfold parseSteps finFile stepsOf
  cases hs : j.steps with
  | nil => simp [noopStep, noopName]
  | cons a t =>
    simp only [List.isEmpty_cons, Bool.false_eq_true, ite_false, Bool.false_and, List.map_cons]
    simp only [List.map_map, Function.comp_def, parseStep_wstep] at *

theorem parse_main (p : Pipe) (j : Core.Job) (hon : j.onFailure = false) :
    parseFile (mainFile p j) = wpStripMain j := by
  have hst := parseSteps_main p j
  unfold parseFile wpStripMain
  rw [hst]
  cases hm : j.manual <;> simp [mainFile, hon, Function.comp_def, hm]

theorem parse_fin (p : Pipe) (j : Core.Job) (hman : j.manual = false) (hne : (baseEntries p) ≠ []) :
    parseFile (finFile p j) = wpStripFin j := by
  have hst := parseSteps_fin p j
  unfold parseFile wpStripFin
  rw [hst]
  obtain ⟨e, es, he⟩ : ∃ e es, baseEntries p = e :: es := by
    cases h : baseEntries p with
    | nil => exact absurd h hne
    | cons e es => exact ⟨e, es, rfl⟩
  have hhead : (finFile p j).whens.head? = some { e with status := finStatus j } := by
    simp [finFile, he]
  simp only [hhead, Option.map_some]
  simp [finFile, hman, finStatus]

theorem sharedTrigs_of (sel : List WFile) (W : Option (List Trig)) (x : WFile) (r : List WFile)
    (hs : sel = x :: r) (h : ∀ y ∈ sel, whenTrigs y = W) : sharedTrigs sel = W := by
  subst hs
  have hx : whenTrigs x = W := h x (by simp)
  have hr : r.all (fun y => decide (whenTrigs y = whenTrigs x)) = true := by
    rw [List.all_eq_true]
    intro y hy
    have := h y (by simp [hy])
    simp [this, hx]
  simp only [sharedTrigs, List.map_cons]
  rw [ite_eq_left (by rw [List.all_map]; simpa [Function.comp_def] using hr)]
  exact hx

theorem readTriggers_of (fs : List WFile) (W : Option (List Trig)) (T : List Trig) (hne : fs ≠ [])
    (hW : ∀ f ∈ fs, f.manual.isNone = true → whenTrigs f = W)
    (hM : ∀ f ∈ fs, ∀ o, f.manual = some o → o = T) (hT : W = some T ∨ (W = none ∧ T = [])) :
    readTriggers fs = T := by
  unfold readTriggers
  dsimp only
  by_cases hsel : fs.filter (fun f => f.manual.isNone) = []
  · -- every file is manual
    have hall : ∀ f ∈ fs, f.manual.isSome = true := by
      intro f hf
      by_contra hc
      have hn : f.manual.isNone = true := by simpa using hc
      have : f ∈ fs.filter (fun f => f.manual.isNone) := List.mem_filter.2 ⟨hf, hn⟩
      rw [hsel] at this; exact absurd this (by simp)
    obtain ⟨f, rest, rfl⟩ : ∃ f rest, fs = f :: rest := by
      cases fs with
      | nil => exact absurd rfl hne
      | cons f rest => exact ⟨f, rest, rfl⟩
    obtain ⟨o, ho⟩ : ∃ o, f.manual = some o := by
      have := hall f (by simp)
      cases h : f.manual with
      | none => simp [h] at this
      | some o => exact ⟨o, rfl⟩
    have hoT := hM f (by simp) o ho
    rw [hsel]
    simp [ho, hoT]
  · obtain ⟨x, r, hxr⟩ : ∃ x r, fs.filter (fun f => f.manual.isNone) = x :: r := by
      cases h : fs.filter (fun f => f.manual.isNone) with
      | nil => exact absurd h hsel
      | cons x r => exact ⟨x, r, rfl⟩
    have hmem : ∀ y ∈ fs.filter (fun f => f.manual.isNone), whenTrigs y = W := fun y hy =>
      hW y (List.mem_filter.1 hy).1 (List.mem_filter.1 hy).2
    have hsh := sharedTrigs_of _ W x r hxr hmem
    rw [hsh, hxr]
    rcases hT with hT | ⟨hW0, hT0⟩
    · subst hT
      simp only [List.isEmpty_cons, Bool.not_false, Option.isNone_some, Bool.and_false, Bool.false_eq_true,
        ite_false]
      cases hf : fs.findSome? (·.manual) with
      | none => simp
      | some o =>
        obtain ⟨l₁, a, l₂, hl, hfa, _⟩ := List.findSome?_eq_some_iff.1 hf
        exact hM a (by rw [hl]; simp) o hfa
    · subst hW0; subst hT0
      simp

theorem entryTrig_trigEntry (t : Trig) (hk : t.kind = kindPr ∨ t.kind = kindPush ∨ t.kind = kindTag)
    (hx : ∀ x ∈ t.incl, ExactPat x) : entryTrig (trigEntry t) = some t := by
  obtain ⟨k, l⟩ := t
  simp only at hk hx
  rcases hk with rfl | rfl | rfl <;>
    simp [entryTrig, trigEntry, kindPr, kindPush, kindTag, evPr, evPush, evTag] <;> exact hx

theorem mem_trigsOf {p : Pipe} {t : Trig} (h : t ∈ trigsOf p) :
    (∃ l, p.pr = some l ∧ t = ⟨kindPr, l⟩) ∨ (∃ l, p.push = some l ∧ t = ⟨kindPush, l⟩) ∨
    (∃ l, p.tag = some l ∧ t = ⟨kindTag, l⟩) := by
  unfold trigsOf at h
  simp only [List.mem_append] at h
  rcases h with (h | h) | h
  · left
    cases hp : p.pr with
    | none => simp [hp] at h
    | some l => simp [hp] at h; exact ⟨l, rfl, h⟩
  · right; left
    cases hp : p.push with
    | none => simp [hp] at h
    | some l => simp [hp] at h; exact ⟨l, rfl, h⟩
  · right; right
    cases hp : p.tag with
    | none => simp [hp] at h
    | some l => simp [hp] at h; exact ⟨l, rfl, h⟩

theorem trig_ok {p : Pipe} (hex : ∀ x ∈ trigPats p, ExactPat x) {t : Trig} (h : t ∈ trigsOf p) :
    (t.kind = kindPr ∨ t.kind = kindPush ∨ t.kind = kindTag) ∧ ∀ x ∈ t.incl, ExactPat x := by
  rcases mem_trigsOf h with ⟨l, hl, rfl⟩ | ⟨l, hl, rfl⟩ | ⟨l, hl, rfl⟩
  · exact ⟨Or.inl rfl, fun x hx => hex x (by simp [trigPats, hl, hx])⟩
  · exact ⟨Or.inr (Or.inl rfl), fun x hx => hex x (by simp [trigPats, hl, hx])⟩
  · exact ⟨Or.inr (Or.inr rfl), fun x hx => hex x (by simp [trigPats, hl, hx])⟩

theorem mapM_trigEntry (l : List Trig) (h : ∀ t ∈ l, entryTrig (trigEntry t) = some t) :
    (l.map trigEntry).mapM entryTrig = some l := by
  induction l with
  | nil => rfl
  | cons a t ih =>
    rw [List.map_cons, List.mapM_cons, h a (by simp), ih fun x hx => h x (by simp [hx])]
    rfl

theorem baseEntries_status (p : Pipe) : ∀ e ∈ baseEntries p, e.status = [] := by
  intro e he
  unfold baseEntries at he
  split at he
  · simp at he; subst he; rfl
  · obtain ⟨t, _, rfl⟩ := List.mem_map.1 he
    unfold trigEntry; split <;> rfl

theorem baseEntries_mapM (p : Pipe) (hex : ∀ x ∈ trigPats p, ExactPat x) :
    (baseEntries p).mapM entryTrig = some (trigsOf p) ∨
    ((baseEntries p).mapM entryTrig = none ∧ trigsOf p = []) := by
  unfold baseEntries
  by_cases h : trigsOf p = []
  · right
    refine ⟨?_, h⟩
    simp [h, entryTrig, allEvents, evPush, evPr, evTag]
  · left
    rw [ite_eq_right h]
    exact mapM_trigEntry _ fun t ht => entryTrig_trigEntry t (trig_ok hex ht).1 (trig_ok hex ht).2

theorem view_main (p : Pipe) (j : Core.Job) : (mainFile p j).view = (mainFile p j).whens := by
  simp [WFile.view, mainFile]

theorem view_fin (p : Pipe) (j : Core.Job) : (finFile p j).view = baseEntries p := by
  have hid : ∀ e ∈ baseEntries p, ({ { e with status := finStatus j } with status := [] } : Entry) = e := by
    intro e he
    have hs := baseEntries_status p e he
    cases e
    simp_all
  simp only [WFile.view, finFile, Option.isSome_some, ite_true]
  rw [List.map_map]
  conv_rhs => rw [← List.map_id (baseEntries p)]
  exact List.map_congr_left hid

theorem baseEntries_ne (p : Pipe) : baseEntries p ≠ [] := by
  unfold baseEntries
  split
  · simp
  · rename_i h; simpa using h

theorem readTriggers_toWp (p : Pipe) (hex : ∀ x ∈ trigPats p, ExactPat x) (hne : p.all ≠ []) :
    readTriggers (toWp p) = trigsOf p := by
  apply readTriggers_of (toWp p) ((baseEntries p).mapM entryTrig) (trigsOf p)
  · intro h
    apply hne
    unfold toWp at h
    simp only [List.append_eq_nil_iff, List.map_eq_nil_iff] at h
    simp [Pipe.all, h.1, h.2]
  · intro f hf hn
    unfold toWp at hf
    rcases List.mem_append.1 hf with h | h
    · obtain ⟨j, _, rfl⟩ := List.mem_map.1 h
      cases hm : j.manual
      · unfold whenTrigs; rw [view_main]; simp [mainFile, hm]
      · simp [mainFile, hm] at hn
    · obtain ⟨j, _, rfl⟩ := List.mem_map.1 h
      unfold whenTrigs; rw [view_fin]
  · intro f hf o ho
    unfold toWp at hf
    rcases List.mem_append.1 hf with h | h
    · obtain ⟨j, _, rfl⟩ := List.mem_map.1 h
      cases hm : j.manual <;> simp [mainFile, hm] at ho
      exact ho.symm
    · obtain ⟨j, _, rfl⟩ := List.mem_map.1 h
      simp [finFile] at ho
  · exact baseEntries_mapM p hex

theorem trigFind_trigsOf (p : Pipe) :
    trigFind kindPush (trigsOf p) = p.push ∧ trigFind kindTag (trigsOf p) = p.tag ∧
    trigFind kindPr (trigsOf p) = p.pr := by
  obtain ⟨id, name, push, tag, pr, jobs, fin⟩ := p
  rcases push with _ | l1 <;> rcases tag with _ | l2 <;> rcases pr with _ | l3 <;>
    simp [trigFind, trigsOf, kindPr, kindPush, kindTag]

theorem filter_main (p : Pipe) : (toWp p).filter (fun f => f.fin.isNone) = p.jobs.map (mainFile p) := by
  unfold toWp
  rw [List.filter_append]
  have h1 : (p.jobs.map (mainFile p)).filter (fun f => f.fin.isNone) = p.jobs.map (mainFile p) :=
    List.filter_eq_self.2 (by intro x hx; obtain ⟨j, _, rfl⟩ := List.mem_map.1 hx; simp [mainFile])
  have h2 : (p.fin.map (finFile p)).filter (fun f => f.fin.isNone) = [] :=
    List.filter_eq_nil_iff.2 (by intro x hx; obtain ⟨j, _, rfl⟩ := List.mem_map.1 hx; simp [finFile])
  rw [h1, h2, List.append_nil]

theorem filter_fin (p : Pipe) : (toWp p).filter (fun f => f.fin.isSome) = p.fin.map (finFile p) := by
  unfold toWp
  rw [List.filter_append]
  have h1 : (p.jobs.map (mainFile p)).filter (fun f => f.fin.isSome) = [] :=
    List.filter_eq_nil_iff.2 (by intro x hx; obtain ⟨j, _, rfl⟩ := List.mem_map.1 hx; simp [mainFile])
  have h2 : (p.fin.map (finFile p)).filter (fun f => f.fin.isSome) = p.fin.map (finFile p) :=
    List.filter_eq_self.2 (by intro x hx; obtain ⟨j, _, rfl⟩ := List.mem_map.1 hx; simp [finFile])
  rw [h1, h2, List.nil_append]

theorem rawWp_toWp (p : Pipe) (hw : Wf p) (ho : WpOk p) :
    rawWp (toWp p) =
      { id := wpPipelineId, name := "", push := p.push, tag := p.tag, pr := p.pr,
        jobs := p.jobs.map wpStripMain, fin := p.fin.map wpStripFin } := by
  obtain ⟨_, _, _, _, _, _, _, _, _, hon, hman⟩ := hw
  obtain ⟨_, hex, hne, _⟩ := ho
  have ht := readTriggers_toWp p hex hne
  obtain ⟨h1, h2, h3⟩ := trigFind_trigsOf p
  unfold rawWp
  rw [ht, h1, h2, h3, filter_main, filter_fin, List.map_map, List.map_map]
  congr 1
  · exact List.map_congr_left fun j hj => parse_main p j (hon j hj)
  · exact List.map_congr_left fun j hj => parse_fin p j (hman j hj) (baseEntries_ne p)

theorem restoreStep_wp (p : Pipe) (j : Core.Job) (hj : j ∈ p.all) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : (j.steps.map Core.Step.id).Nodup) (s : Core.Step) (hsj : s ∈ j.steps) :
    restoreStep (metaOf p) p.id j.id (stripName s) = s := by
  have hf : findJob p j.id = some j := find_id Core.Job.id p.all j hj hn
  have hst : findStep j s.id = some s := find_id Core.Step.id j.steps s hsj hs
  unfold restoreStep stripName
  simp only [metaOf, hf, hst, ite_true, Option.bind_some]
  rw [fill_nameOf]

theorem restoreSteps_wp (p : Pipe) (j : Core.Job) (hj : j ∈ p.all) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : (j.steps.map Core.Step.id).Nodup) :
    (j.steps.map stripName).map (restoreStep (wpMeta p) p.id j.id) = j.steps := by
  rw [List.map_map]
  apply map_self_mem
  intro s hsj
  exact restoreStep_wp p j hj hn hs s hsj

theorem wpMeta_fields (p : Pipe) (j : Core.Job) (hf : findJob p j.id = some j) :
    (wpMeta p).runner (.job p.id j.id) = (if j.runner = [] then none else some j.runner) ∧
    (wpMeta p).retry (.job p.id j.id) = (if j.retry = 0 then none else some j.retry) ∧
    (wpMeta p).timeout (.job p.id j.id) = (if j.timeout = 0 then none else some j.timeout) ∧
    (wpMeta p).names (.job p.id j.id) = nameOf j.name ∧
    (wpMeta p).needs (.job p.id j.id) = (if j.needs.any (·.artifacts) = true then some j.needs else none) := by
  refine ⟨?_, ?_, ?_, ?_, ?_⟩ <;> simp [wpMeta, metaOf, hf]

theorem runner_rt (r : List String) :
    ((if r = [] then (none : Option (List String)) else some r).getD (platformOf r).toList) = r := by
  split <;> simp_all [platformOf]

theorem retry_rt (r : Nat) : ((if r = 0 then (none : Option Nat) else some r).getD 0) = r := by
  split <;> simp_all

theorem restoreJob_wpMain (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : ∀ j ∈ p.all, (j.steps.map Core.Step.id).Nodup) (j : Core.Job) (hj : j ∈ p.all) :
    restoreJob (wpMeta p) p.id (wpStripMain j) = j := by
  have hf : findJob p j.id = some j := find_id Core.Job.id p.all j hj hn
  obtain ⟨hr, hy, ht, hnm, hnd⟩ := wpMeta_fields p j hf
  have hsteps := restoreSteps_wp p j hj hn (hs j hj)
  have hneeds' : (if j.needs.any (·.artifacts) = true then some j.needs else none).getD
      (j.needs.map fun n => { n with artifacts := false }) = j.needs := by
    by_cases hany : j.needs.any (·.artifacts) = true
    · simp [hany]
    · have hall : ∀ n ∈ j.needs, n.artifacts = false := by
        intro n hn'
        simpa using fun h => hany (List.any_eq_true.mpr ⟨n, hn', h⟩)
      simp only [hany]
      exact map_self_mem _ j.needs (fun n hn' => by
        have := hall n hn'
        cases n; simp_all)
  unfold restoreJob wpStripMain
  simp only [hnm, hnd, hr, hy, ht, fill_nameOf, hneeds', hsteps]
  simp only [runner_rt, retry_rt]

theorem restoreJob_wpFin (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : ∀ j ∈ p.all, (j.steps.map Core.Step.id).Nodup) (j : Core.Job) (hj : j ∈ p.all) :
    restoreJob (wpMeta p) p.id (wpStripFin j) = j := by
  have hf : findJob p j.id = some j := find_id Core.Job.id p.all j hj hn
  obtain ⟨hr, hy, ht, hnm, hnd⟩ := wpMeta_fields p j hf
  have hsteps := restoreSteps_wp p j hj hn (hs j hj)
  have hneeds' : (if j.needs.any (·.artifacts) = true then some j.needs else none).getD j.needs = j.needs := by
    split <;> simp
  unfold restoreJob wpStripFin
  simp only [hnm, hnd, hr, hy, ht, fill_nameOf, hneeds', hsteps]
  simp only [runner_rt, retry_rt]

/-- Theorem 1 of the Woodpecker adapter: parsing the emitted files and applying the metadata returns the
pipeline, on the Woodpecker fragment. -/
theorem woodpecker_from_to (p : Pipe) (h : WpFrag p) : fromWp (toWp p) (wpMeta p) = p := by
  obtain ⟨hw, ho⟩ := h
  have hraw := rawWp_toWp p hw ho
  obtain ⟨_, hnd, _, _, _, _, _, hsteps, _, _, _⟩ := hw
  have hn : (p.all.map Core.Job.id).Nodup := hnd
  unfold fromWp
  rw [hraw]
  have hjs : ∀ j ∈ p.jobs, restoreJob (wpMeta p) p.id (wpStripMain j) = j := fun j hj =>
    restoreJob_wpMain p hn (fun j hj => (hsteps j hj).2) j (by simp [Pipe.all, hj])
  have hfs : ∀ j ∈ p.fin, restoreJob (wpMeta p) p.id (wpStripFin j) = j := fun j hj =>
    restoreJob_wpFin p hn (fun j hj => (hsteps j hj).2) j (by simp [Pipe.all, hj])
  have hid : (wpMeta p).ids wpPipelineId = if p.id ≠ wpPipelineId then some p.id else none := by
    simp [wpMeta]
  have hpid : ((wpMeta p).ids wpPipelineId).getD wpPipelineId = p.id := by
    rw [hid]; by_cases h : p.id = wpPipelineId <;> simp [h]
  have hnm : fill "" ((wpMeta p).names (.pipe p.id)) = p.name := by
    simp only [wpMeta, metaOf, ite_true]; exact fill_nameOf _
  unfold restore
  simp only [hpid, hnm, List.map_map]
  have e1 : List.map (restoreJob (wpMeta p) p.id ∘ wpStripMain) p.jobs = p.jobs :=
    map_self_mem _ p.jobs fun j hj => hjs j hj
  have e2 : List.map (restoreJob (wpMeta p) p.id ∘ wpStripFin) p.fin = p.fin :=
    map_self_mem _ p.fin fun j hj => hfs j hj
  rw [e1, e2]

/-! ### The raw parse keeps the fragment, so a chain can run through Woodpecker -/

/-- The ID the Woodpecker parser assigns is a valid job key. -/
theorem validId_wpPipelineId : validId wpPipelineId := by
  have hl : wpPipelineId.toLower = "woodpecker" := by unfold wpPipelineId; decide +kernel
  have ht : wpPipelineId.toList = ['w', 'o', 'o', 'd', 'p', 'e', 'c', 'k', 'e', 'r'] := by
    unfold wpPipelineId; decide +kernel
  refine ⟨?_, ?_, ?_, ?_⟩
  · rw [ht]; simp
  · rw [ht]
    intro c hc
    simp only [List.take, List.mem_singleton] at hc
    subst hc; left; decide +kernel
  · rw [ht]
    intro c hc
    simp only [List.mem_cons, List.not_mem_nil, or_false] at hc
    rcases hc with rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl
    all_goals (left; decide +kernel)
  · show wpPipelineId.toLower ∉ gitlabReserved
    rw [hl]; decide +kernel

/-- What the strips keep of a job: everything `Wf`, `WpOk` and `GitlabOk` read. -/
def Same (j x : Core.Job) : Prop :=
  x.id = j.id ∧ x.needs.map (·.job) = j.needs.map (·.job) ∧ x.env = j.env ∧ x.steps = j.steps.map stripName ∧
  x.manual = j.manual ∧ x.onFailure = j.onFailure ∧ (x.needs = [] ↔ j.needs = []) ∧ x.retry ≤ 3

theorem same_main (j : Core.Job) : Same j (wpStripMain j) := by
  refine ⟨rfl, ?_, rfl, rfl, rfl, rfl, ?_, by simp [wpStripMain]⟩
  · simp [wpStripMain, List.map_map, Function.comp_def]
  · simp [wpStripMain]

theorem same_fin (j : Core.Job) : Same j (wpStripFin j) :=
  ⟨rfl, rfl, rfl, rfl, rfl, rfl, Iff.rfl, by simp [wpStripFin]⟩

theorem mem_stripName {j : Core.Job} {s : Core.Step} (h : s ∈ j.steps.map stripName) :
    ∃ s0 ∈ j.steps, s.id = s0.id ∧ s.script = s0.script ∧ s.env = s0.env := by
  obtain ⟨s0, hs0, rfl⟩ := List.mem_map.1 h
  exact ⟨s0, hs0, rfl, rfl, rfl⟩

theorem same_needs_mem {j x : Core.Job} (hs : Same j x) {n : Core.Need} (hn : n ∈ x.needs) :
    ∃ n0 ∈ j.needs, n0.job = n.job := by
  have : n.job ∈ x.needs.map (·.job) := List.mem_map_of_mem (f := fun n : Core.Need => n.job) hn
  rw [hs.2.1] at this
  obtain ⟨n0, h0, e⟩ := List.mem_map.1 this
  exact ⟨n0, h0, e⟩

theorem rawWp_frag (p : Pipe) (hw : Wf p) (ho : WpOk p) :
    Wf (rawWp (toWp p)) ∧ WpOk (rawWp (toWp p)) ∧ (GhaOk p → GhaOk (rawWp (toWp p))) ∧
      (GitlabOk p → GitlabOk (rawWp (toWp p))) := by
  rw [rawWp_toWp p hw ho]
  obtain ⟨h0, h1, h2, h3, h4, h5, h6, h7, h8, h9, h10⟩ := hw
  obtain ⟨hm, hx, hne, hjo⟩ := ho
  have allm : ∀ x ∈ p.jobs.map wpStripMain ++ p.fin.map wpStripFin, ∃ j ∈ p.all, Same j x := by
    intro x hx
    rcases List.mem_append.1 hx with h | h
    · obtain ⟨j, hj, rfl⟩ := List.mem_map.1 h
      exact ⟨j, by simp [Pipe.all, hj], same_main j⟩
    · obtain ⟨j, hj, rfl⟩ := List.mem_map.1 h
      exact ⟨j, by simp [Pipe.all, hj], same_fin j⟩
  have jobsm : ∀ x ∈ p.jobs.map wpStripMain, ∃ j ∈ p.jobs, Same j x := by
    intro x hx
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 hx
    exact ⟨j, hj, same_main j⟩
  have finm : ∀ x ∈ p.fin.map wpStripFin, ∃ j ∈ p.fin, Same j x := by
    intro x hx
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 hx
    exact ⟨j, hj, same_fin j⟩
  have hids : jobIds (p.jobs.map wpStripMain ++ p.fin.map wpStripFin) = jobIds p.all := by
    simp [jobIds, Pipe.all, wpStripMain, wpStripFin, List.map_map, Function.comp_def]
  have hdag : dagJobs (p.jobs.map wpStripMain) = dagJobs p.jobs := by
    unfold dagJobs
    simp [wpStripMain, List.map_map, Function.comp_def]
  have hjobs : jobIds (p.jobs.map wpStripMain) = jobIds p.jobs := by
    simp [jobIds, wpStripMain, List.map_map, Function.comp_def]
  have hall' : (p.jobs.map wpStripMain ++ p.fin.map wpStripFin) ≠ [] := by
    intro h
    apply hne
    simp only [List.append_eq_nil_iff, List.map_eq_nil_iff] at h
    simp [Pipe.all, h.1, h.2]
  have hstepsId : ∀ j x : Core.Job, Same j x → x.steps.map Core.Step.id = j.steps.map Core.Step.id := by
    intro j x hs
    rw [hs.2.2.2.1]
    simp [List.map_map, Function.comp_def, stripName]
  have hW : Wf ({ id := wpPipelineId, name := "", push := p.push, tag := p.tag, pr := p.pr, jobs := p.jobs.map wpStripMain, fin := p.fin.map wpStripFin } : Pipe) := by
    refine ⟨validId_wpPipelineId, ?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_⟩
    · show (jobIds (p.jobs.map wpStripMain ++ p.fin.map wpStripFin)).Nodup
      rw [hids]; exact h1
    · intro x hx
      obtain ⟨j, hj, hs⟩ := allm x hx
      rw [hs.1]; exact h2 j hj
    · show (Dag.layers (dagJobs (p.jobs.map wpStripMain))).isSome = true
      rw [hdag]; exact h3
    · intro x hx n hn
      obtain ⟨j, hj, hs⟩ := jobsm x hx
      obtain ⟨n0, hn0, e⟩ := same_needs_mem hs hn
      show n.job ∈ jobIds (p.jobs.map wpStripMain)
      rw [hjobs, ← e]; exact h4 j hj n0 hn0
    · intro x hx n hn
      obtain ⟨j, hj, hs⟩ := finm x hx
      obtain ⟨n0, hn0, e⟩ := same_needs_mem hs hn
      show n.job ∈ jobIds (p.jobs.map wpStripMain)
      rw [hjobs, ← e]; exact h5 j hj n0 hn0
    · intro x hx
      obtain ⟨j, hj, hs⟩ := allm x hx
      rw [hs.2.1]; exact h6 j hj
    · intro x hx
      obtain ⟨j, hj, hs⟩ := allm x hx
      rw [hs.2.2.1, hstepsId j x hs]; exact h7 j hj
    · intro x hx s hs'
      obtain ⟨j, hj, hs⟩ := allm x hx
      rw [hs.2.2.2.1] at hs'
      obtain ⟨s0, hs0, e1, _, e3⟩ := mem_stripName hs'
      rw [e1, e3]; exact h8 j hj s0 hs0
    · intro x hx
      obtain ⟨j, hj, hs⟩ := jobsm x hx
      rw [hs.2.2.2.2.2.1]; exact h9 j hj
    · intro x hx
      obtain ⟨j, hj, hs⟩ := finm x hx
      rw [hs.2.2.2.2.1]; exact h10 j hj
  refine ⟨hW, ⟨?_, hx, hall', ?_⟩, ?_, ?_⟩
  · intro x hx hman
    obtain ⟨j, hj, hs⟩ := allm x hx
    rw [hs.2.2.2.2.1] at hman
    exact hm j hj hman
  · intro x hx
    obtain ⟨j, hj, hs⟩ := allm x hx
    refine ⟨by rw [hs.2.2.1]; exact (hjo j hj).1, ?_⟩
    intro s hs'
    rw [hs.2.2.2.1] at hs'
    obtain ⟨s0, hs0, _, e2, e3⟩ := mem_stripName hs'
    rw [e2, e3]; exact (hjo j hj).2 s0 hs0
  · intro hg x hx
    obtain ⟨j, hj, hs⟩ := allm x hx
    obtain ⟨hst, hsc⟩ := hg j hj
    refine ⟨by rw [hs.2.2.2.1]; simpa using hst, ?_⟩
    intro s hs'
    rw [hs.2.2.2.1] at hs'
    obtain ⟨s0, hs0, _, e2, _⟩ := mem_stripName hs'
    rw [e2]; exact hsc s0 hs0
  · intro hg
    obtain ⟨hany, hretry, hfree⟩ := hg
    refine ⟨hany, ?_, ?_⟩
    · intro x hx
      obtain ⟨j, _, hs⟩ := allm x hx
      exact hs.2.2.2.2.2.2.2
    · intro x hx hnil y hy
      obtain ⟨j, hj, hs⟩ := finm x hx
      obtain ⟨k, hk, hsk⟩ := allm y hy
      have := hfree j hj (hs.2.2.2.2.2.2.1.1 hnil) k hk
      exact hsk.2.2.2.2.2.2.1.2 this

end Adapter

end Woodpecker
end Pipemaster
