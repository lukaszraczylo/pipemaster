import Pipemaster.Core
import Pipemaster.Matrix
import Pipemaster.Woodpecker

namespace Pipemaster
namespace Drone

/-! ## The adapter on the core subset

`DDoc` mirrors the keys the emitter writes for one pipeline document of `.drone.yml` (provider/drone), not YAML
syntax. The text fields hold the written strings, so the `$` to `$$` escape of command, variable and filter text is
in the model (`esc`, `unesc`). Each marker of the document is a field, because the parser reads markers by path
(collapse.go). Two codecs stay outside the model and the Go vector test checks them on the vector alphabet only:
the YAML quoting of every scalar and the `refs/tags/` prefix of a tag filter (the model keeps the glob).

Not in the model, each on purpose:
- A pipeline with no job: the emitter writes a document named `empty`, which the parser drops (`DroneOk`).
- Expressions, actions, services, containers, caches, artifacts, outputs, environments, concurrency: the subset of
  `Core`. The `job.container` marker of every document (the default image of its steps) and the `image` key of every
  step carry no core field, and the vector test checks that they are the constants of the emitter.
- Several pipelines in one file (multi-pipeline markers), `resources` documents, signature documents.
- Matrices are in the section `Matrix`: the core subset has no matrix, so the section states the cell documents and
  their collapse for one job. Its lemmas prove cell counts and fold behaviour only. Matrix jobs are outside the
  `drone_from_to` round-trip theorem.
- Trigger combinations that need a `ref` list (several kinds with different filters) or that Drone cannot join into
  one condition (the document is then written disabled): `TrigOk`. -/

section Adapter

open Core

/-- One `trigger` block: the Drone conditions of a document. `ref` holds the tag globs without `refs/tags/`. -/
structure DTrig where
  events : List String
  branch : List String
  ref : List String
  status : List String
  deriving DecidableEq, Repr

/-- One written step. `ignore` is the `failure: ignore` key. -/
structure DStep where
  name : String
  cmds : List String
  env : List Var
  ignore : Bool
  deriving DecidableEq, Repr

/-- One pipeline document; `name` is the job id. `env` is the document `environment`, `noop` the `step.script`
marker, `allowFailure` the `job.allow_failure` marker, `manual` the `job.manual` marker with the pipeline triggers,
`fin` the `pipeline.finally` marker (needs and `noIf`), `ifTrigs` the triggers of the `job.if` marker, and
`platform` the label that `platform` holds (`os/arch` or `os/arch/variant`). -/
structure DDoc where
  name : String
  trigger : Option DTrig
  deps : List String
  steps : List DStep
  env : List Var
  platform : Option String
  noop : Bool
  allowFailure : Bool
  manual : Option (List Woodpecker.Trig)
  fin : Option (List Core.Need × Bool)
  ifTrigs : Option (List Woodpecker.Trig)
  deriving DecidableEq, Repr

open Woodpecker (Trig trigsOf kindPr kindPush kindTag)

/-! ### Constants of the emitter and parser -/

/-- Mirrors drone.pipelineID (provider/drone/dialect.go): the ID Parse gives its one pipeline. -/
def dronePipelineId : String := "drone"
/-- Mirrors drone.placeholderName (provider/drone/emit_step.go). -/
def noopName : String := "noop"
/-- The command of the placeholder step (emit_step.go placeholder). -/
def noopCmd : String := "true"
/-- Mirrors drone.emptyJobID (provider/drone/emit.go). -/
def emptyJobId : String := "empty"
/-- Mirrors drone.maxName (provider/drone/ids.go): the longest document or step name. -/
def maxName : Nat := 100
/-- Mirrors drone.eventPush (provider/drone/parse_when.go). -/
def evPush : String := "push"
/-- Mirrors drone.eventPR (provider/drone/parse_when.go). -/
def evPr : String := "pull_request"
/-- Mirrors drone.eventTag (provider/drone/parse_when.go). -/
def evTag : String := "tag"
/-- Mirrors drone.eventCustom (provider/drone/parse_when.go): the event of a manual document. -/
def evCustom : String := "custom"
/-- Mirrors drone.statusOK (provider/drone/emit_cond.go). -/
def statusOk : String := "success"
/-- Mirrors drone.statusFail (provider/drone/emit_cond.go). -/
def statusFail : String := "failure"

/-! ### Text: the `$` escape -/

/-- Doubles every `$`. -/
def escL : List Char → List Char
  | [] => []
  | c :: t => (if c = '$' then ['$', '$'] else [c]) ++ escL t

/-- Turns each `$$` into `$`, scanning left to right. -/
def unescL : List Char → List Char
  | '$' :: '$' :: t => '$' :: unescL t
  | c :: t => c :: unescL t
  | [] => []

/-- Mirrors drone.esc (provider/drone/emit_text.go): the `$` of a text reaches Drone's pre-processor as `$$`. -/
def esc (s : String) : String := String.ofList (escL s.toList)

/-- Mirrors drone.unescape (provider/drone/text.go): `$$` becomes `$`. -/
def unesc (s : String) : String := String.ofList (unescL s.toList)

theorem unescL_ne (c : Char) (h : c ≠ '$') (r : List Char) : unescL (c :: r) = c :: unescL r := by
  cases r with
  | nil => simp [unescL]
  | cons d t => simp [unescL, h]

theorem unescL_escL (l : List Char) : unescL (escL l) = l := by
  induction l with
  | nil => rfl
  | cons c t ih =>
    by_cases h : c = '$'
    · subst h; simp [escL, unescL, ih]
    · simp only [escL, h, ite_false, List.singleton_append]
      rw [unescL_ne c h, ih]

/-- The escape is exact for every text: `unesc` undoes `esc`. -/
theorem unesc_esc (s : String) : unesc (esc s) = s := by
  simp [esc, unesc, unescL_escL]

def escVar (v : Var) : Var := ⟨v.name, esc v.value⟩
def unescVar (v : Var) : Var := ⟨v.name, unesc v.value⟩

theorem unescVar_escVar (v : Var) : unescVar (escVar v) = v := by
  cases v; simp [unescVar, escVar, unesc_esc]

theorem map_unesc_esc (l : List String) : (l.map esc).map unesc = l := by
  induction l with
  | nil => rfl
  | cons a t ih => simp [unesc_esc, ih]

theorem map_comp_unesc_esc (l : List String) : l.map (unesc ∘ esc) = l := by
  simpa [List.map_map] using map_unesc_esc l

theorem map_unescVar_escVar (l : List Var) : (l.map escVar).map unescVar = l := by
  induction l with
  | nil => rfl
  | cons a t ih => simp [unescVar_escVar, ih]

theorem map_comp_unescVar_escVar (l : List Var) : l.map (unescVar ∘ escVar) = l := by
  simpa [List.map_map] using map_unescVar_escVar l

/-! ### Emission -/

/-- Mirrors drone.platformRE (provider/drone/emit_job.go): the label `os/arch` or `os/arch/variant`. -/
def platformOf (r : List String) : Option String := Woodpecker.platformOf r

/-- The label that the parser reads back: `os` and `arch` only; the variant stays in Ext (parse_doc.go platform). -/
def platDrop (l : String) : String := "/".intercalate ((l.splitOn "/").take 2)

/-- The events of the pipeline triggers in the order of `triggerSet` (emit_cond.go events): push, pull request, tag. -/
def evsOf (p : Pipe) : List String :=
  (if p.push.isSome then [evPush] else []) ++ (if p.pr.isSome then [evPr] else []) ++
  (if p.tag.isSome then [evTag] else [])

/-- The branch filter: push and pull request share one list (`TrigOk`). -/
def branchOf (p : Pipe) : List String :=
  match p.push, p.pr with
  | some f, _ => f
  | none, some f => f
  | none, none => []

/-- Mirrors drone.triggerSet (provider/drone/emit_cond.go) on `TrigOk` pipelines: one event list, and the
branch list or the tag list as the one filter. No trigger writes no block. -/
def trigBlock (p : Pipe) : Option DTrig :=
  if trigsOf p = [] then none
  else some ⟨evsOf p, (branchOf p).map esc, (p.tag.getD []).map esc, []⟩

/-- The block of a manual document (emit_cond.go jobSet, `j.Manual`). -/
def manualBlock : DTrig := ⟨[evCustom], [], [], []⟩

/-- The block of a finally document: the trigger block plus the status gate (emit_cond.go jobSet, `finally`). A
job that runs only after a failure takes the status of its `failure()` condition instead. -/
def finBlock (p : Pipe) (j : Core.Job) : DTrig :=
  { (trigBlock p).getD ⟨[], [], [], []⟩ with
    status := if j.onFailure then [statusFail] else [statusOk, statusFail] }

def dstep (j : Core.Job) (s : Core.Step) : DStep :=
  ⟨s.id, s.script.map esc, s.env.map escVar, j.allowFailure⟩

/-- The step that stands in for a job with no step (emit_step.go placeholder): it has no `failure` key. -/
def noopStep : DStep := ⟨noopName, [noopCmd], [], false⟩

def stepsOf (j : Core.Job) : List DStep := if j.steps.isEmpty then [noopStep] else j.steps.map (dstep j)

/-- A main document: its needs are the dependencies, and a manual job replaces its trigger with `custom`. -/
def mainDoc (p : Pipe) (j : Core.Job) : DDoc where
  name := j.id
  trigger := if j.manual then some manualBlock else trigBlock p
  deps := j.needs.map (·.job)
  steps := stepsOf j
  env := j.env.map escVar
  platform := platformOf j.runner
  noop := j.steps.isEmpty
  allowFailure := j.allowFailure
  manual := if j.manual then some (trigsOf p) else none
  fin := none
  ifTrigs := none

/-- Mirrors drone.dependsOn (provider/drone/emit_job.go): a finally document waits for every main document.
The needs of the job stay in the `pipeline.finally` marker. A job that runs only after a failure merges its
condition with the triggers, so the file marks the original triggers (emit_cond.go markCond). -/
def finDoc (p : Pipe) (j : Core.Job) : DDoc where
  name := j.id
  trigger := some (finBlock p j)
  deps := p.jobs.map (·.id)
  steps := stepsOf j
  env := j.env.map escVar
  platform := platformOf j.runner
  noop := j.steps.isEmpty
  allowFailure := j.allowFailure
  manual := none
  fin := some (j.needs, !j.onFailure)
  ifTrigs := if j.onFailure ∧ trigsOf p ≠ [] then some (trigsOf p) else none

/-- Mirrors drone.Emit (provider/drone/emit.go) on the core subset: one document per job, main jobs first. -/
def toDrone (p : Pipe) : List DDoc := p.jobs.map (mainDoc p) ++ p.fin.map (finDoc p)

/-! ### Parsing -/

/-- Mirrors drone.pureTriggers (provider/drone/parse_when.go): the pipeline triggers of a block that is a pure
trigger filter. A status, no event, a `ref` filter on anything but the tag event, or a branch filter on the tag
event is not one. The model reads the push, pull request and tag events; a `custom` event is a manual gate. -/
def pureEv (t : DTrig) (ev : String) : Option Trig :=
  if ev = evTag then (if t.branch ≠ [] then none else some ⟨kindTag, t.ref.map unesc⟩)
  else if ev = evPush then some ⟨kindPush, t.branch.map unesc⟩
  else if ev = evPr then some ⟨kindPr, t.branch.map unesc⟩
  else none

def pureOf (t : DTrig) : Option (List Trig) :=
  if t.status ≠ [] ∨ t.events = [] then none
  else if t.ref ≠ [] ∧ ¬ (t.events = [evTag] ∧ t.branch = []) then none
  else t.events.eraseDups.mapM (pureEv t)

/-- Mirrors drone.pure (provider/drone/collapse_group.go): the triggers of a document. A merged job condition
brings its marker triggers; a finally document drops its status gate before it reads the block. -/
def pureDoc (d : DDoc) : Option (List Trig) :=
  match d.ifTrigs with
  | some ts => some ts
  | none =>
    match d.trigger with
    | none => none
    | some t =>
      match pureOf t with
      | some r => some r
      | none => if d.fin.isSome ∧ t.status = [statusOk, statusFail] then pureOf { t with status := [] } else none

/-- The trigger list that every document of `sel` agrees on (collapse_group.go sharedTriggers). -/
def sharedTrigs (sel : List DDoc) : Option (List Trig) :=
  match sel.map pureDoc with
  | [] => none
  | x :: r => if r.all (fun y => decide (y = x)) then x else none

/-- Mirrors drone.placeTriggers (provider/drone/collapse_group.go): the triggers the non-manual documents
share, with the fallback to the documents that hold no merged condition, replaced by the list of a manual
document's marker. -/
def pickShared (sel : List DDoc) : Option (List Trig) :=
  match sharedTrigs sel with
  | some l => some l
  | none =>
    let plain := sel.filter fun (d : DDoc) => d.ifTrigs.isNone
    if plain ≠ [] ∧ plain.length < sel.length then sharedTrigs plain else none

def readTriggers (docs : List DDoc) : List Trig :=
  match docs.findSome? (·.manual) with
  | some orig => orig
  | none => (pickShared (docs.filter fun (d : DDoc) => d.manual.isNone)).getD []

def parseStepD (s : DStep) : Core.Step := ⟨s.name, "", s.cmds.map unesc, s.env.map unescVar⟩

/-- Mirrors drone.collapseNoop (provider/drone/collapse.go): the placeholder step goes when the marker names it. -/
def parseSteps (d : DDoc) : List Core.Step :=
  match d.steps with
  | [s] => if d.noop && s.name == noopName && s.cmds == [noopCmd] then [] else [parseStepD s]
  | ss => ss.map parseStepD

/-- A finally job is `onFailure` when its status gate is `failure` alone (`failure()` is its condition). -/
def parseDoc (d : DDoc) : Core.Job where
  id := d.name
  name := ""
  needs := match d.fin with
    | some (ns, _) => ns
    | none => d.deps.map fun n => ⟨n, false⟩
  env := d.env.map unescVar
  runner := (d.platform.map platDrop).toList
  steps := parseSteps d
  retry := 0
  timeout := 0
  manual := d.manual.isSome
  allowFailure := d.allowFailure
  onFailure := d.fin.isSome && decide ((d.trigger.map (·.status)) = some [statusFail])

/-- The pipeline Parse builds before the round-trip metadata is applied. -/
def rawDrone (docs : List DDoc) : Pipe :=
  { id := dronePipelineId
    name := ""
    push := Woodpecker.trigFind kindPush (readTriggers docs)
    tag := Woodpecker.trigFind kindTag (readTriggers docs)
    pr := Woodpecker.trigFind kindPr (readTriggers docs)
    jobs := (docs.filter fun d => d.fin.isNone).map parseDoc
    fin := (docs.filter fun d => d.fin.isSome).map parseDoc }

/-- The metadata layer of the Drone adapter: the core layer, the pipeline rename to `drone` (roundtrip.IDsFor), and
the runner labels, retry runs and timeout of each job, which the emitter reports lost or external
(caps.JobRunner, caps.JobRetry, caps.JobTimeout). It is the Woodpecker layer with another pipeline ID. -/
def droneMeta (p : Pipe) : Meta :=
  { Woodpecker.wpMeta p with
    ids := fun q => if q = dronePipelineId ∧ p.id ≠ dronePipelineId then some p.id else none }

/-- Mirrors drone.Parse (provider/drone/parse.go) on the core subset; the round-trip metadata `m` is applied last
(roundtrip.Restore). -/
def fromDrone (docs : List DDoc) (m : Meta) : Pipe := restore (rawDrone docs) m

/-! ### The fragment -/

/-- A `$` that no name or brace follows. A `$NAME` or `${NAME}` may be a Drone variable that the parser reads as a
reference (text.go parts), which changes the text. -/
def dollarSafe : List Char → Bool
  | '$' :: c :: t => !(c.isAlpha || c = '_' || c = '{') && dollarSafe (c :: t)
  | _ :: t => dollarSafe t
  | [] => true

/-- The start of a shell guard line, which the parser takes off a script (parse_step.go script; expr/drone/guard.go
guardOpen). -/
def guardStart : List Char := ['i', 'f', ' ', '!', ' ']

/-- A text that names the file of a Tekton result (`$(results.`). Outside Tekton the text is a command substitution
that fails, so the emitter points the result file at /dev/null and reports job.outputs lost (emitkit result.go
NeutralizeResultWrite). The model
refuses the whole prefix, which is stricter than the matcher. -/
def resultOpen : List Char → Bool
  | '$' :: '(' :: 'r' :: 'e' :: 's' :: 'u' :: 'l' :: 't' :: 's' :: '.' :: _ => true
  | _ :: t => resultOpen t
  | [] => false

/-- A script line the model keeps. A newline joins into one multi-line command that the parser splits again
(emit_step.go commandTexts, parse_step.go script). A blank line folds into the line before it (emit_step.go
foldBlankLines). A line that opens like a guard is read as a condition (parse_step.go script). -/
def LineOk (l : String) : Prop :=
  '\n' ∉ l.toList ∧ l ≠ "" ∧ dollarSafe l.toList = true ∧ guardStart.isPrefixOf l.toList = false ∧
    resultOpen l.toList = false

/-- A variable value the codec keeps: a newline breaks the YAML scalar, and `$NAME` is a Drone substitution
(text.go value). -/
def ValueOk (v : String) : Prop := '\n' ∉ v.toList ∧ dollarSafe v.toList = true

instance (l : String) : Decidable (LineOk l) := by unfold LineOk; exact inferInstance
instance (v : String) : Decidable (ValueOk v) := by unfold ValueOk; exact inferInstance

/-- The trigger shapes that one Drone condition holds (emit_cond.go triggerSet): no filter at all; a branch filter
shared by the push and pull request triggers, with no tag trigger and none of them empty; or a tag filter alone.
Other mixes are written as a ref list, or as a disabled document when no list joins them. -/
def TrigOk (p : Pipe) : Prop :=
  (p.tag.getD [] ≠ [] → p.push = none ∧ p.pr = none) ∧
  ((p.push.getD [] ≠ [] ∨ p.pr.getD [] ≠ []) →
    p.tag = none ∧ (p.push.isSome → p.push.getD [] ≠ []) ∧ (p.pr.isSome → p.pr.getD [] ≠ []) ∧
      (p.push.isSome → p.pr.isSome → p.push = p.pr))

instance (p : Pipe) : Decidable (TrigOk p) := by unfold TrigOk; exact inferInstance

/-- What Drone needs beyond `Wf`:
- the pipeline has a job (otherwise the emitter writes a document `empty`);
- a job named `empty` has a step (the parser drops a document of that name with no step);
- job and step names fit `maxName`;
- trigger patterns are exact (emit_cond.go exactGlob; `Woodpecker.ExactPat` refuses a glob character) and the triggers have the shape of
  `TrigOk`;
- every step has a script of lines the codec keeps, and variable values hold no newline or `$NAME`. -/
def DroneOk (p : Pipe) : Prop :=
  p.all ≠ [] ∧ TrigOk p ∧ (∀ x ∈ Woodpecker.trigPats p, Woodpecker.ExactPat x) ∧
  (∀ j ∈ p.all, j.id.length ≤ maxName ∧ (j.id = emptyJobId → j.steps ≠ []) ∧ (∀ v ∈ j.env, ValueOk v.value) ∧
    ∀ s ∈ j.steps, s.id.length ≤ maxName ∧ s.script ≠ [] ∧ (∀ l ∈ s.script, LineOk l) ∧
      ∀ v ∈ s.env, ValueOk v.value)

instance (p : Pipe) : Decidable (DroneOk p) := by unfold DroneOk; exact inferInstance

/-- The Drone fragment (Mirrors drone.Caps (provider/drone/caps.go)): the shared structure plus Drone's own needs. -/
def DroneFrag (p : Pipe) : Prop := Wf p ∧ DroneOk p

instance (p : Pipe) : Decidable (DroneFrag p) := by unfold DroneFrag; exact inferInstance

/-! ### Theorem 1: parse inverts emit on the Drone fragment -/

/-- The triggers `pureOf` reads from the block of `trigBlock`, in event order. -/
def pureList (p : Pipe) : List Trig :=
  (p.push.map fun l => (⟨kindPush, l⟩ : Trig)).toList ++ (p.pr.map fun l => (⟨kindPr, l⟩ : Trig)).toList ++
  (p.tag.map fun l => (⟨kindTag, l⟩ : Trig)).toList

theorem pureOf_trigBlock (p : Pipe) (ht : TrigOk p) (hne : trigsOf p ≠ []) :
    pureOf ⟨evsOf p, (branchOf p).map esc, (p.tag.getD []).map esc, []⟩ = some (pureList p) := by
  obtain ⟨h1, h2⟩ := ht
  obtain ⟨id, name, push, tag, pr, jobs, fin⟩ := p
  simp only at h1 h2 hne ⊢
  rcases push with _ | f <;> rcases pr with _ | g <;> rcases tag with _ | h <;>
    simp [pureOf, pureList, evsOf, branchOf, Woodpecker.trigsOf, evPush, evPr, evTag, kindPr, kindPush, kindTag] at * <;>
    simp [List.eraseDups, List.eraseDupsBy, List.eraseDupsBy.loop, pureEv, evPush, evPr, evTag, kindPr, kindPush, kindTag, map_comp_unesc_esc] <;> simp_all
  by_cases hf : f = [] <;> by_cases hg : g = [] <;> simp_all

theorem pureOf_status (t : DTrig) (h : t.status ≠ []) : pureOf t = none := by
  simp [pureOf, h]

theorem pureDoc_main (p : Pipe) (j : Core.Job) (hm : j.manual = false) (ht : TrigOk p) :
    pureDoc (mainDoc p j) = if trigsOf p = [] then none else some (pureList p) := by
  by_cases hT : trigsOf p = []
  · simp [pureDoc, mainDoc, hm, trigBlock, hT]
  · simp [pureDoc, mainDoc, hm, trigBlock, hT, pureOf_trigBlock p ht hT]

theorem pureDoc_fin (p : Pipe) (j : Core.Job) (ht : TrigOk p) :
    pureDoc (finDoc p j) =
      if trigsOf p = [] then none else if j.onFailure then some (trigsOf p) else some (pureList p) := by
  by_cases hT : trigsOf p = []
  · cases hf : j.onFailure <;>
      simp [pureDoc, finDoc, finBlock, trigBlock, hT, hf, pureOf, statusFail, statusOk]
  · cases hf : j.onFailure
    · simp [pureDoc, finDoc, finBlock, trigBlock, hT, hf, pureOf_status, statusFail, statusOk,
        pureOf_trigBlock p ht hT]
    · simp [pureDoc, finDoc, finBlock, trigBlock, hT, hf]

theorem sharedTrigs_const (sel : List DDoc) (L : List Trig) (hne : sel ≠ []) (h : ∀ d ∈ sel, pureDoc d = some L) :
    sharedTrigs sel = some L := by
  cases sel with
  | nil => exact absurd rfl hne
  | cons x r =>
    have hx := h x (by simp)
    have hr : r.all (fun y => decide (pureDoc y = pureDoc x)) = true := by
      rw [List.all_eq_true]
      intro y hy
      simp [h y (by simp [hy]), hx]
    simp only [sharedTrigs, List.map_cons]
    rw [ite_eq_left (by rw [List.all_map]; simpa [Function.comp_def] using hr), hx]

theorem sharedTrigs_mem (sel : List DDoc) (l : List Trig) (h : sharedTrigs sel = some l) :
    ∃ d ∈ sel, pureDoc d = some l := by
  cases sel with
  | nil => simp [sharedTrigs] at h
  | cons x r =>
    refine ⟨x, by simp, ?_⟩
    simp only [sharedTrigs, List.map_cons] at h
    split at h
    · exact h
    · simp at h

theorem pickShared_ok (sel : List DDoc) (L0 T : List Trig) (hne : sel ≠ [])
    (hd : ∀ d ∈ sel, (d.ifTrigs ≠ none ∧ pureDoc d = some T) ∨ (d.ifTrigs = none ∧ pureDoc d = some L0)) :
    pickShared sel = some L0 ∨ pickShared sel = some T := by
  unfold pickShared
  cases hs : sharedTrigs sel with
  | some l =>
    obtain ⟨d, hdm, hdv⟩ := sharedTrigs_mem sel l hs
    rcases hd d hdm with ⟨_, h⟩ | ⟨_, h⟩
    · right; rw [h] at hdv; simpa using hdv.symm
    · left; rw [h] at hdv; simpa using hdv.symm
  | none =>
    simp only
    by_cases hc : (sel.filter fun (d : DDoc) => d.ifTrigs.isNone) ≠ [] ∧
        (sel.filter fun (d : DDoc) => d.ifTrigs.isNone).length < sel.length
    · left
      rw [ite_eq_left hc]
      apply sharedTrigs_const _ _ hc.1
      intro d hdm
      have := List.mem_filter.1 hdm
      rcases hd d this.1 with ⟨h1, _⟩ | ⟨_, h⟩
      · simp [Option.isNone_iff_eq_none] at this
        exact absurd this.2 h1
      · exact h
    · exfalso
      by_cases hp : (sel.filter fun (d : DDoc) => d.ifTrigs.isNone) = []
      · have hall : ∀ d ∈ sel, pureDoc d = some T := by
          intro d hdm
          rcases hd d hdm with ⟨_, h⟩ | ⟨h1, _⟩
          · exact h
          · have : d ∈ sel.filter fun (d : DDoc) => d.ifTrigs.isNone := List.mem_filter.2 ⟨hdm, by simp [h1]⟩
            rw [hp] at this; simp at this
        rw [sharedTrigs_const sel T hne hall] at hs; simp at hs
      · have hlen : (sel.filter fun (d : DDoc) => d.ifTrigs.isNone).length = sel.length := by
          have := List.length_filter_le (fun (d : DDoc) => d.ifTrigs.isNone) sel
          have : ¬ (sel.filter fun (d : DDoc) => d.ifTrigs.isNone).length < sel.length := fun h => hc ⟨hp, h⟩
          omega
        have hall' := List.length_filter_eq_length_iff.1 hlen
        have hall : ∀ d ∈ sel, pureDoc d = some L0 := by
          intro d hdm
          have h1 : d.ifTrigs = none := by simpa [Option.isNone_iff_eq_none] using hall' d hdm
          rcases hd d hdm with ⟨h2, _⟩ | ⟨_, h⟩
          · exact absurd h1 h2
          · exact h
        rw [sharedTrigs_const sel L0 hne hall] at hs; simp at hs

theorem sharedTrigs_none (sel : List DDoc) (h : ∀ d ∈ sel, pureDoc d = none) : sharedTrigs sel = none := by
  cases sel with
  | nil => simp [sharedTrigs]
  | cons x r =>
    simp only [sharedTrigs, List.map_cons, h x (by simp)]
    split <;> rfl

theorem pickShared_none (sel : List DDoc) (h : ∀ d ∈ sel, pureDoc d = none) : pickShared sel = none := by
  unfold pickShared
  rw [sharedTrigs_none sel h]
  simp only
  split
  · exact sharedTrigs_none _ fun d hd => h d (List.mem_filter.1 hd).1
  · rfl

theorem trigsOf_nil {p : Pipe} (h : trigsOf p = []) : p.push = none ∧ p.tag = none ∧ p.pr = none := by
  obtain ⟨id, name, push, tag, pr, jobs, fin⟩ := p
  rcases push with _ | l1 <;> rcases tag with _ | l2 <;> rcases pr with _ | l3 <;>
    simp_all [Woodpecker.trigsOf]

theorem trigFind_pureList (p : Pipe) :
    Woodpecker.trigFind kindPush (pureList p) = p.push ∧ Woodpecker.trigFind kindTag (pureList p) = p.tag ∧
    Woodpecker.trigFind kindPr (pureList p) = p.pr := by
  obtain ⟨id, name, push, tag, pr, jobs, fin⟩ := p
  rcases push with _ | l1 <;> rcases tag with _ | l2 <;> rcases pr with _ | l3 <;>
    simp [Woodpecker.trigFind, pureList, kindPr, kindPush, kindTag]

theorem readTriggers_toDrone (p : Pipe) (ht : TrigOk p) (hne : p.all ≠ []) :
    Woodpecker.trigFind kindPush (readTriggers (toDrone p)) = p.push ∧
    Woodpecker.trigFind kindTag (readTriggers (toDrone p)) = p.tag ∧
    Woodpecker.trigFind kindPr (readTriggers (toDrone p)) = p.pr := by
  have hdocs : toDrone p ≠ [] := by
    intro h
    apply hne
    unfold toDrone at h
    simp only [List.append_eq_nil_iff, List.map_eq_nil_iff] at h
    simp [Pipe.all, h.1, h.2]
  unfold readTriggers
  cases hf : (toDrone p).findSome? (·.manual) with
  | some o =>
    obtain ⟨l₁, a, l₂, hl, hfa, _⟩ := List.findSome?_eq_some_iff.1 hf
    have hmem : a ∈ toDrone p := by rw [hl]; simp
    have ho : o = trigsOf p := by
      unfold toDrone at hmem
      rcases List.mem_append.1 hmem with h | h
      · obtain ⟨j, _, rfl⟩ := List.mem_map.1 h
        cases hm : j.manual <;> simp [mainDoc, hm] at hfa
        exact hfa.symm
      · obtain ⟨j, _, rfl⟩ := List.mem_map.1 h
        simp [finDoc] at hfa
    simp only [ho]
    exact Woodpecker.trigFind_trigsOf p
  | none =>
    have hmain : ∀ j ∈ p.jobs, j.manual = false := by
      intro j hj
      have : (mainDoc p j).manual = none := by
        have h := List.findSome?_eq_none_iff.1 hf
        exact h _ (List.mem_append.2 (Or.inl (List.mem_map.2 ⟨j, hj, rfl⟩)))
      cases hm : j.manual
      · rfl
      · simp [mainDoc, hm] at this
    have hsel : (toDrone p).filter (fun (d : DDoc) => d.manual.isNone) = toDrone p := by
      apply List.filter_eq_self.2
      intro d hd
      have := List.findSome?_eq_none_iff.1 hf d hd
      simp [this]
    simp only [hsel]
    by_cases hT : trigsOf p = []
    · obtain ⟨h1, h2, h3⟩ := trigsOf_nil hT
      have hn : ∀ d ∈ toDrone p, pureDoc d = none := by
        intro d hd
        unfold toDrone at hd
        rcases List.mem_append.1 hd with h | h
        · obtain ⟨j, hj, rfl⟩ := List.mem_map.1 h
          simp [pureDoc_main p j (hmain j hj) ht, hT]
        · obtain ⟨j, _, rfl⟩ := List.mem_map.1 h
          simp [pureDoc_fin p j ht, hT]
      rw [pickShared_none _ hn]
      simp [Woodpecker.trigFind, h1, h2, h3]
    · have hd : ∀ d ∈ toDrone p, (d.ifTrigs ≠ none ∧ pureDoc d = some (trigsOf p)) ∨
          (d.ifTrigs = none ∧ pureDoc d = some (pureList p)) := by
        intro d hd
        unfold toDrone at hd
        rcases List.mem_append.1 hd with h | h
        · obtain ⟨j, hj, rfl⟩ := List.mem_map.1 h
          refine Or.inr ⟨rfl, ?_⟩
          rw [pureDoc_main p j (hmain j hj) ht]
          simp [hT]
        · obtain ⟨j, _, rfl⟩ := List.mem_map.1 h
          cases hj : j.onFailure
          · refine Or.inr ⟨by simp [finDoc, hj], ?_⟩
            rw [pureDoc_fin p j ht]
            simp [hT, hj]
          · refine Or.inl ⟨by simp [finDoc, hj, hT], ?_⟩
            rw [pureDoc_fin p j ht]
            simp [hT, hj]
      rcases pickShared_ok _ (pureList p) (trigsOf p) hdocs hd with h | h
      · rw [h]; exact trigFind_pureList p
      · rw [h]; exact Woodpecker.trigFind_trigsOf p

/-- What Drone keeps of a main job before the metadata: no names, no artifacts flag on needs, no retry or timeout,
and no runner label but the `os/arch` platform. -/
def droneStripMain (j : Core.Job) : Core.Job :=
  { j with name := "", needs := j.needs.map fun n => { n with artifacts := false },
           runner := ((platformOf j.runner).map platDrop).toList,
           steps := j.steps.map Woodpecker.stripName, retry := 0, timeout := 0 }

/-- A finally job keeps its needs with the flags, because the `pipeline.finally` marker carries them. -/
def droneStripFin (j : Core.Job) : Core.Job :=
  { j with name := "", runner := ((platformOf j.runner).map platDrop).toList,
           steps := j.steps.map Woodpecker.stripName, retry := 0, timeout := 0 }

theorem parseStepD_dstep (j : Core.Job) (s : Core.Step) : parseStepD (dstep j s) = Woodpecker.stripName s := by
  simp [parseStepD, dstep, Woodpecker.stripName, List.map_map, map_comp_unesc_esc, map_comp_unescVar_escVar]

theorem parseSteps_of (d : DDoc) (j : Core.Job) (hd : d.steps = j.steps.map (dstep j)) (hn : d.noop = false) :
    parseSteps d = j.steps.map Woodpecker.stripName := by
  unfold parseSteps
  rw [hd]
  match hs : j.steps with
  | [] => rfl
  | [a] => simp [hn, parseStepD_dstep]
  | a :: b :: t => simp [parseStepD_dstep]

theorem parseSteps_noop (d : DDoc) (j : Core.Job) (hd : d.steps = stepsOf j) (hn : d.noop = j.steps.isEmpty) :
    parseSteps d = j.steps.map Woodpecker.stripName := by
  cases hs : j.steps with
  | nil => simp [parseSteps, hd, hn, hs, stepsOf, noopStep, noopName, noopCmd]
  | cons a t =>
    rw [← hs]
    exact parseSteps_of d j (by simp [hd, stepsOf, hs]) (by simp [hn, hs])

theorem parse_main (p : Pipe) (j : Core.Job) (hon : j.onFailure = false) :
    parseDoc (mainDoc p j) = droneStripMain j := by
  have hst := parseSteps_noop (mainDoc p j) j rfl rfl
  unfold parseDoc droneStripMain
  rw [hst]
  cases hm : j.manual <;>
    simp [mainDoc, hon, hm, List.map_map, map_comp_unescVar_escVar, platformOf]

theorem parse_fin (p : Pipe) (j : Core.Job) (hman : j.manual = false) :
    parseDoc (finDoc p j) = droneStripFin j := by
  have hst := parseSteps_noop (finDoc p j) j rfl rfl
  unfold parseDoc droneStripFin
  rw [hst]
  cases hf : j.onFailure <;>
    simp [finDoc, finBlock, hman, hf, statusOk, statusFail, map_comp_unescVar_escVar, platformOf]

theorem filter_main (p : Pipe) : (toDrone p).filter (fun d => d.fin.isNone) = p.jobs.map (mainDoc p) := by
  unfold toDrone
  rw [List.filter_append]
  have h1 : (p.jobs.map (mainDoc p)).filter (fun d => d.fin.isNone) = p.jobs.map (mainDoc p) :=
    List.filter_eq_self.2 (by intro x hx; obtain ⟨j, _, rfl⟩ := List.mem_map.1 hx; simp [mainDoc])
  have h2 : (p.fin.map (finDoc p)).filter (fun d => d.fin.isNone) = [] :=
    List.filter_eq_nil_iff.2 (by intro x hx; obtain ⟨j, _, rfl⟩ := List.mem_map.1 hx; simp [finDoc])
  rw [h1, h2, List.append_nil]

theorem filter_fin (p : Pipe) : (toDrone p).filter (fun d => d.fin.isSome) = p.fin.map (finDoc p) := by
  unfold toDrone
  rw [List.filter_append]
  have h1 : (p.jobs.map (mainDoc p)).filter (fun d => d.fin.isSome) = [] :=
    List.filter_eq_nil_iff.2 (by intro x hx; obtain ⟨j, _, rfl⟩ := List.mem_map.1 hx; simp [mainDoc])
  have h2 : (p.fin.map (finDoc p)).filter (fun d => d.fin.isSome) = p.fin.map (finDoc p) :=
    List.filter_eq_self.2 (by intro x hx; obtain ⟨j, _, rfl⟩ := List.mem_map.1 hx; simp [finDoc])
  rw [h1, h2, List.nil_append]

theorem rawDrone_toDrone (p : Pipe) (hw : Wf p) (ho : DroneOk p) :
    rawDrone (toDrone p) =
      { id := dronePipelineId, name := "", push := p.push, tag := p.tag, pr := p.pr,
        jobs := p.jobs.map droneStripMain, fin := p.fin.map droneStripFin } := by
  obtain ⟨_, _, _, _, _, _, _, _, _, hon, hman⟩ := hw
  obtain ⟨hne, ht, _, _⟩ := ho
  obtain ⟨h1, h2, h3⟩ := readTriggers_toDrone p ht hne
  unfold rawDrone
  rw [h1, h2, h3, filter_main, filter_fin, List.map_map, List.map_map]
  congr 1
  · exact List.map_congr_left fun j hj => parse_main p j (hon j hj)
  · exact List.map_congr_left fun j hj => parse_fin p j (hman j hj)

theorem droneMeta_fields (p : Pipe) (j : Core.Job) (hf : findJob p j.id = some j) :
    (droneMeta p).runner (.job p.id j.id) = (if j.runner = [] then none else some j.runner) ∧
    (droneMeta p).retry (.job p.id j.id) = (if j.retry = 0 then none else some j.retry) ∧
    (droneMeta p).timeout (.job p.id j.id) = (if j.timeout = 0 then none else some j.timeout) ∧
    (droneMeta p).names (.job p.id j.id) = nameOf j.name ∧
    (droneMeta p).needs (.job p.id j.id) = (if j.needs.any (·.artifacts) = true then some j.needs else none) :=
  Woodpecker.wpMeta_fields p j hf

theorem restoreSteps_drone (p : Pipe) (j : Core.Job) (hj : j ∈ p.all) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : (j.steps.map Core.Step.id).Nodup) :
    (j.steps.map Woodpecker.stripName).map (restoreStep (droneMeta p) p.id j.id) = j.steps :=
  Woodpecker.restoreSteps_wp p j hj hn hs

theorem runner_rt (r : List String) :
    ((if r = [] then (none : Option (List String)) else some r).getD ((platformOf r).map platDrop).toList) = r := by
  split <;> simp_all [platformOf, Woodpecker.platformOf]

theorem restoreJob_droneMain (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : ∀ j ∈ p.all, (j.steps.map Core.Step.id).Nodup) (j : Core.Job) (hj : j ∈ p.all) :
    restoreJob (droneMeta p) p.id (droneStripMain j) = j := by
  have hf : findJob p j.id = some j := find_id Core.Job.id p.all j hj hn
  obtain ⟨hr, hy, ht, hnm, hnd⟩ := droneMeta_fields p j hf
  have hsteps := restoreSteps_drone p j hj hn (hs j hj)
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
  unfold restoreJob droneStripMain
  simp only [hnm, hnd, hr, hy, ht, fill_nameOf, hneeds', hsteps]
  simp only [runner_rt, Woodpecker.retry_rt]

theorem restoreJob_droneFin (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : ∀ j ∈ p.all, (j.steps.map Core.Step.id).Nodup) (j : Core.Job) (hj : j ∈ p.all) :
    restoreJob (droneMeta p) p.id (droneStripFin j) = j := by
  have hf : findJob p j.id = some j := find_id Core.Job.id p.all j hj hn
  obtain ⟨hr, hy, ht, hnm, hnd⟩ := droneMeta_fields p j hf
  have hsteps := restoreSteps_drone p j hj hn (hs j hj)
  have hneeds' : (if j.needs.any (·.artifacts) = true then some j.needs else none).getD j.needs = j.needs := by
    split <;> simp
  unfold restoreJob droneStripFin
  simp only [hnm, hnd, hr, hy, ht, fill_nameOf, hneeds', hsteps]
  simp only [runner_rt, Woodpecker.retry_rt]

/-- Theorem 1 of the Drone adapter: parsing the emitted documents and applying the metadata returns the pipeline,
on the Drone fragment. -/
theorem drone_from_to (p : Pipe) (h : DroneFrag p) : fromDrone (toDrone p) (droneMeta p) = p := by
  obtain ⟨hw, ho⟩ := h
  have hraw := rawDrone_toDrone p hw ho
  obtain ⟨_, hnd, _, _, _, _, _, hsteps, _, _, _⟩ := hw
  have hn : (p.all.map Core.Job.id).Nodup := hnd
  unfold fromDrone
  rw [hraw]
  have hjs : ∀ j ∈ p.jobs, restoreJob (droneMeta p) p.id (droneStripMain j) = j := fun j hj =>
    restoreJob_droneMain p hn (fun j hj => (hsteps j hj).2) j (by simp [Pipe.all, hj])
  have hfs : ∀ j ∈ p.fin, restoreJob (droneMeta p) p.id (droneStripFin j) = j := fun j hj =>
    restoreJob_droneFin p hn (fun j hj => (hsteps j hj).2) j (by simp [Pipe.all, hj])
  have hid : (droneMeta p).ids dronePipelineId = if p.id ≠ dronePipelineId then some p.id else none := by
    simp [droneMeta]
  have hpid : ((droneMeta p).ids dronePipelineId).getD dronePipelineId = p.id := by
    rw [hid]; by_cases h : p.id = dronePipelineId <;> simp [h]
  have hnm : fill "" ((droneMeta p).names (.pipe p.id)) = p.name := by
    simp only [droneMeta, Woodpecker.wpMeta, metaOf, ite_true]; exact fill_nameOf _
  unfold restore
  simp only [hpid, hnm, List.map_map]
  have e1 : List.map (restoreJob (droneMeta p) p.id ∘ droneStripMain) p.jobs = p.jobs :=
    map_self_mem _ p.jobs fun j hj => hjs j hj
  have e2 : List.map (restoreJob (droneMeta p) p.id ∘ droneStripFin) p.fin = p.fin :=
    map_self_mem _ p.fin fun j hj => hfs j hj
  rw [e1, e2]

/-! ### The raw parse keeps the fragment, so a chain can run through Drone -/

/-- The ID the Drone parser assigns is a valid job key. -/
theorem validId_dronePipelineId : validId dronePipelineId := by
  have hl : dronePipelineId.toLower = "drone" := by unfold dronePipelineId; decide +kernel
  have ht : dronePipelineId.toList = ['d', 'r', 'o', 'n', 'e'] := by
    unfold dronePipelineId; decide +kernel
  refine ⟨?_, ?_, ?_, ?_⟩
  · rw [ht]; simp
  · rw [ht]
    intro c hc
    simp only [List.take, List.mem_singleton] at hc
    subst hc; left; decide +kernel
  · rw [ht]
    intro c hc
    simp only [List.mem_cons, List.not_mem_nil, or_false] at hc
    rcases hc with rfl | rfl | rfl | rfl | rfl
    all_goals (left; decide +kernel)
  · show dronePipelineId.toLower ∉ gitlabReserved
    rw [hl]; decide +kernel

/-- What a raw parse keeps of a job: everything `Wf`, `WpOk`, `DroneOk` and `GitlabOk` read. -/
def Keep (j x : Core.Job) : Prop :=
  x.id = j.id ∧ x.needs.map (·.job) = j.needs.map (·.job) ∧ x.env = j.env ∧
  x.steps = j.steps.map Woodpecker.stripName ∧ x.manual = j.manual ∧ x.onFailure = j.onFailure ∧
  (x.needs = [] ↔ j.needs = []) ∧ (j.retry ≤ 3 → x.retry ≤ 3)

theorem keep_needs_mem {j x : Core.Job} (hs : Keep j x) {n : Core.Need} (hn : n ∈ x.needs) :
    ∃ n0 ∈ j.needs, n0.job = n.job := by
  have : n.job ∈ x.needs.map (·.job) := List.mem_map_of_mem (f := fun n : Core.Need => n.job) hn
  rw [hs.2.1] at this
  obtain ⟨n0, h0, e⟩ := List.mem_map.1 this
  exact ⟨n0, h0, e⟩

/-- A pipeline whose jobs keep what `Keep` lists keeps each shared predicate of the input. -/
theorem frag_of_shape (p : Pipe) (pid : String) (hpid : validId pid) (fm ff : Core.Job → Core.Job)
    (hm : ∀ j, Keep j (fm j)) (hf : ∀ j, Keep j (ff j)) (q : Pipe)
    (hq : q = { id := pid, name := "", push := p.push, tag := p.tag, pr := p.pr,
                jobs := p.jobs.map fm, fin := p.fin.map ff }) :
    (Wf p → Wf q) ∧ (Woodpecker.WpOk p → Woodpecker.WpOk q) ∧ (DroneOk p → DroneOk q) ∧
      (GhaOk p → GhaOk q) ∧ (GitlabOk p → GitlabOk q) := by
  have allm : ∀ x ∈ q.all, ∃ j ∈ p.all, Keep j x := by
    intro x hx
    subst hq
    simp only [Pipe.all, List.mem_append, List.mem_map] at hx ⊢
    rcases hx with ⟨j, hj, rfl⟩ | ⟨j, hj, rfl⟩
    · exact ⟨j, Or.inl hj, hm j⟩
    · exact ⟨j, Or.inr hj, hf j⟩
  have jobsm : ∀ x ∈ q.jobs, ∃ j ∈ p.jobs, Keep j x := by
    intro x hx
    subst hq
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 hx
    exact ⟨j, hj, hm j⟩
  have finm : ∀ x ∈ q.fin, ∃ j ∈ p.fin, Keep j x := by
    intro x hx
    subst hq
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 hx
    exact ⟨j, hj, hf j⟩
  have hmid : ∀ j, (fm j).id = j.id := fun j => (hm j).1
  have hfid : ∀ j, (ff j).id = j.id := fun j => (hf j).1
  have hmneeds : ∀ j, (fm j).needs.map (fun n => (⟨n.job, false⟩ : Pipemaster.Need)) =
      j.needs.map (fun n => (⟨n.job, false⟩ : Pipemaster.Need)) := by
    intro j
    have := congrArg (List.map fun (s : String) => (⟨s, false⟩ : Pipemaster.Need)) (hm j).2.1
    simpa [List.map_map, Function.comp_def] using this
  have hids : jobIds (p.jobs.map fm ++ p.fin.map ff) = jobIds p.all := by
    simp [jobIds, Pipe.all, List.map_map, Function.comp_def, hmid, hfid]
  have hjobs : jobIds (p.jobs.map fm) = jobIds p.jobs := by
    simp [jobIds, List.map_map, Function.comp_def, hmid]
  have hdag : dagJobs (p.jobs.map fm) = dagJobs p.jobs := by
    simp [dagJobs, List.map_map, Function.comp_def, hmid, hmneeds]
  have hstepsId : ∀ j x : Core.Job, Keep j x → x.steps.map Core.Step.id = j.steps.map Core.Step.id := by
    intro j x hs
    rw [hs.2.2.2.1]
    simp [List.map_map, Function.comp_def, Woodpecker.stripName]
  refine ⟨?_, ?_, ?_, ?_, ?_⟩
  · intro hw
    obtain ⟨h0, h1, h2, h3, h4, h5, h6, h7, h8, h9, h10⟩ := hw
    subst hq
    refine ⟨hpid, ?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_⟩
    · show (jobIds (p.jobs.map fm ++ p.fin.map ff)).Nodup
      rw [hids]; exact h1
    · intro x hx
      obtain ⟨j, hj, hs⟩ := allm x hx
      rw [hs.1]; exact h2 j hj
    · show (Dag.layers (dagJobs (p.jobs.map fm))).isSome = true
      rw [hdag]; exact h3
    · intro x hx n hn
      obtain ⟨j, hj, hs⟩ := jobsm x hx
      obtain ⟨n0, hn0, e⟩ := keep_needs_mem hs hn
      show n.job ∈ jobIds (p.jobs.map fm)
      rw [hjobs, ← e]; exact h4 j hj n0 hn0
    · intro x hx n hn
      obtain ⟨j, hj, hs⟩ := finm x hx
      obtain ⟨n0, hn0, e⟩ := keep_needs_mem hs hn
      show n.job ∈ jobIds (p.jobs.map fm)
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
      obtain ⟨s0, hs0, e1, _, e3⟩ := Woodpecker.mem_stripName hs'
      rw [e1, e3]; exact h8 j hj s0 hs0
    · intro x hx
      obtain ⟨j, hj, hs⟩ := jobsm x hx
      rw [hs.2.2.2.2.2.1]; exact h9 j hj
    · intro x hx
      obtain ⟨j, hj, hs⟩ := finm x hx
      rw [hs.2.2.2.2.1]; exact h10 j hj
  · intro ho
    obtain ⟨hm', hx, hne, hjo⟩ := ho
    have hall' : q.all ≠ [] := by
      intro h
      apply hne
      subst hq
      simp only [Pipe.all, List.append_eq_nil_iff, List.map_eq_nil_iff] at h ⊢
      exact h
    subst hq
    refine ⟨?_, hx, hall', ?_⟩
    · intro x hx hman
      obtain ⟨j, hj, hs⟩ := allm x hx
      rw [hs.2.2.2.2.1] at hman
      exact hm' j hj hman
    · intro x hx
      obtain ⟨j, hj, hs⟩ := allm x hx
      refine ⟨by rw [hs.2.2.1]; exact (hjo j hj).1, ?_⟩
      intro s hs'
      rw [hs.2.2.2.1] at hs'
      obtain ⟨s0, hs0, _, e2, e3⟩ := Woodpecker.mem_stripName hs'
      rw [e2, e3]; exact (hjo j hj).2 s0 hs0
  · intro ho
    obtain ⟨hne, ht, hx, hjo⟩ := ho
    have hall' : q.all ≠ [] := by
      intro h
      apply hne
      subst hq
      simp only [Pipe.all, List.append_eq_nil_iff, List.map_eq_nil_iff] at h ⊢
      exact h
    subst hq
    refine ⟨hall', ht, hx, ?_⟩
    intro x hx
    obtain ⟨j, hj, hs⟩ := allm x hx
    obtain ⟨hlen, hempty, henv, hst⟩ := hjo j hj
    refine ⟨by rw [hs.1]; exact hlen, ?_, by rw [hs.2.2.1]; exact henv, ?_⟩
    · intro hid
      rw [hs.1] at hid
      have := hempty hid
      rw [hs.2.2.2.1]
      simpa using this
    · intro s hs'
      rw [hs.2.2.2.1] at hs'
      obtain ⟨s0, hs0, e1, e2, e3⟩ := Woodpecker.mem_stripName hs'
      obtain ⟨a, b, c, d⟩ := hst s0 hs0
      exact ⟨by rw [e1]; exact a, by rw [e2]; exact b, by rw [e2]; exact c, by rw [e3]; exact d⟩
  · intro hg x hx
    obtain ⟨j, hj, hs⟩ := allm x hx
    obtain ⟨hst, hsc⟩ := hg j hj
    refine ⟨by rw [hs.2.2.2.1]; simpa using hst, ?_⟩
    intro s hs'
    rw [hs.2.2.2.1] at hs'
    obtain ⟨s0, hs0, _, e2, _⟩ := Woodpecker.mem_stripName hs'
    rw [e2]; exact hsc s0 hs0
  · intro hg
    obtain ⟨hany, hretry, hfree⟩ := hg
    subst hq
    refine ⟨hany, ?_, ?_⟩
    · intro x hx
      obtain ⟨j, hj, hs⟩ := allm x hx
      exact hs.2.2.2.2.2.2.2 (hretry j hj)
    · intro x hx hnil y hy
      obtain ⟨j, hj, hs⟩ := finm x hx
      obtain ⟨k, hk, hsk⟩ := allm y hy
      have := hfree j hj (hs.2.2.2.2.2.2.1.1 hnil) k hk
      exact hsk.2.2.2.2.2.2.1.2 this

theorem keep_droneMain (j : Core.Job) : Keep j (droneStripMain j) := by
  refine ⟨rfl, ?_, rfl, rfl, rfl, rfl, ?_, fun _ => by simp [droneStripMain]⟩
  · simp [droneStripMain, List.map_map, Function.comp_def]
  · simp [droneStripMain]

theorem keep_droneFin (j : Core.Job) : Keep j (droneStripFin j) :=
  ⟨rfl, rfl, rfl, rfl, rfl, rfl, Iff.rfl, fun _ => by simp [droneStripFin]⟩

theorem rawDrone_frag (p : Pipe) (hw : Wf p) (ho : DroneOk p) :
    Wf (rawDrone (toDrone p)) ∧ DroneOk (rawDrone (toDrone p)) ∧
      (Woodpecker.WpOk p → Woodpecker.WpOk (rawDrone (toDrone p))) ∧
      (GhaOk p → GhaOk (rawDrone (toDrone p))) ∧ (GitlabOk p → GitlabOk (rawDrone (toDrone p))) := by
  have h := frag_of_shape p dronePipelineId validId_dronePipelineId droneStripMain droneStripFin
    keep_droneMain keep_droneFin _ (rawDrone_toDrone p hw ho)
  exact ⟨h.1 hw, h.2.2.1 ho, h.2.1, h.2.2.2.1, h.2.2.2.2⟩

/-! ### Matrix cells

The Core subset has no matrix, so the cells are modelled over `Matrix.expand`, which the matrix theorems cover.
A matrix job is one document per combination, in `Matrix.expand` order; a matrix with no combination is one
disabled document (emit.go newPlanSet). The parser folds the cells back into the matrix only when they are the
combinations of the marker, in order (collapse_group.go foldCells). -/

section MatrixCells
open Pipemaster.Matrix

/-- Mirrors drone.newPlanSet (provider/drone/emit.go): the cells a matrix job is written as. -/
def cellsOf (m : Matrix) : List Combo := expand m

/-- Mirrors drone.newPlanSet (provider/drone/emit.go): the number of documents of a matrix job. -/
def cellDocCount (m : Matrix) : Nat := max 1 (cellsOf m).length

/-- Mirrors drone.newPlanSet (provider/drone/emit.go): `<job>-<value>-<value>` in key order, before the
de-duplication of names. -/
def cellName (base : String) (c : Combo) : String := "-".intercalate (base :: c.map Prod.snd)

/-- Mirrors drone.foldCells (provider/drone/collapse_group.go): the cells are as emitted. A matrix with no
combination is one document. -/
def foldOk (m : Matrix) (cells : List Combo) : Bool :=
  if (cellsOf m).isEmpty then decide (cells.length ≤ 1) else decide (cells = cellsOf m)

theorem cellDocCount_pos (m : Matrix) : 0 < cellDocCount m := by
  unfold cellDocCount; omega

theorem cellDocCount_eq (m : Matrix) (h : (cellsOf m) ≠ []) :
    cellDocCount m = ((product m.axes).filter (fun c => !excluded m.excludes c)).length
      + (m.includes.filter (fun i => !matchesAny m i)).length := by
  have hl : 0 < (cellsOf m).length := List.length_pos_iff.2 h
  unfold cellDocCount
  rw [Nat.max_eq_right hl]
  exact expand_card m

/-- The folding reads the emitted cells back. -/
theorem foldOk_emitted (m : Matrix) : foldOk m (cellsOf m) = true := by
  unfold foldOk
  split
  · rename_i h
    simp [List.isEmpty_iff.1 h]
  · simp

/-- A cell list that is not the emitted one is refused when the matrix has a combination. -/
theorem foldOk_edited (m : Matrix) (cells : List Combo) (h : cells ≠ cellsOf m) (hne : cellsOf m ≠ []) :
    foldOk m cells = false := by
  unfold foldOk
  have : (cellsOf m).isEmpty = false := by simpa [List.isEmpty_iff] using hne
  simp [this, h]

/-- Every cell comes from the matrix: a merged surviving combination, or an include that merged into none. -/
theorem cell_origin (m : Matrix) (c : Combo) (h : c ∈ cellsOf m) :
    (∃ c0 ∈ survivors m, mergeInto m c0 = c) ∨
      (∃ i ∈ m.includes, matchesAny m i = false ∧ overlay [] i = c) :=
  (expand_mem m c).1 h

end MatrixCells

/-! ### The filter reader and `Match`

`FilterExpr` (expr/drone/filter.go) maps one `when` key to an expression: the include list as a chain of tests
joined by OR, then AND NOT the exclude chain. The theorem below states that the expression holds exactly when
the runner's `Match` accepts the value. The runner is drone/runner-go manifest/cond.go (lines 57-61 at commit
243a9e7, PolyForm Shield): `Match` returns false when an exclude pattern matches, then accepts when the include
list is empty or one of its patterns matches. This file states that rule in its own words and copies no code.
The glob matcher is abstract: it is only assumed to match a pattern with no glob character to itself alone
(expr/drone/filter.go `IsLiteral`, `globMeta`). -/

/-- Mirrors drone.globMeta (expr/drone/filter.go): the characters of the pattern syntax. -/
def globMeta : List Char := ['*', '?', '[', ']', '{', '}', '\\']

/-- Mirrors drone.IsLiteral (expr/drone/filter.go). -/
def isLiteral (v : String) : Bool := v.toList.all fun c => !globMeta.contains c

/-- Mirrors drone.tagRefPrefix (expr/drone/filter.go). -/
def tagPrefix : String := "refs/tags/"

/-- The tag name of a `refs/tags/` value (strings.CutPrefix in filter.go valueExpr). -/
def tagOf (v : String) : Option String :=
  if tagPrefix.toList.isPrefixOf v.toList then some (String.ofList (v.toList.drop tagPrefix.toList.length)) else none

theorem tagOf_some {v t : String} (h : tagOf v = some t) : v = tagPrefix ++ t := by
  unfold tagOf at h
  split at h
  · rename_i hp
    obtain ⟨r, hr⟩ := List.isPrefixOf_iff_prefix.1 hp
    have hd : v.toList.drop tagPrefix.toList.length = r := by
      rw [← hr]; exact List.drop_left
    rw [hd] at h
    have ht : t = String.ofList r := by simpa using h.symm
    apply String.toList_injective
    rw [← hr, ht]
    simp [String.toList_append]
  · simp at h

/-- The expression AST of the filter reader: tests of a context key, a glob and the connectives. -/
inductive FE where
  | eq (k v : String)
  | glob (k pat : String)
  | not (e : FE)
  | and (l r : FE)
  | or (l r : FE)

/-- A `when` key: include patterns, exclude patterns. -/
structure Filt where
  incl : List String
  excl : List String

/-- Mirrors drone.valueExpr (expr/drone/filter.go). -/
def valueE (key v : String) : FE :=
  if isLiteral v then
    match tagOf v with
    | some t => if key = "ref" ∧ t ≠ "" then .eq "tag" t else .eq key v
    | none => .eq key v
  else .glob key v

/-- Mirrors drone.and (expr/drone/filter.go): an absent side is dropped. -/
def andO : Option FE → Option FE → Option FE
  | none, r => r
  | l, none => l
  | some l, some r => some (.and l r)

/-- Mirrors drone.or (expr/drone/filter.go). -/
def orO : Option FE → Option FE → Option FE
  | none, r => r
  | l, none => l
  | some l, some r => some (.or l r)

/-- Mirrors drone.orChain (expr/drone/filter.go) for the keys other than `event`. -/
def orChain (key : String) (vs : List String) : Option FE :=
  vs.foldl (fun o v => orO o (some (valueE key v))) none

/-- Mirrors drone.FilterExpr (expr/drone/filter.go). -/
def filterE (key : String) (f : Filt) : Option FE :=
  let out := if f.incl.isEmpty then none else orChain key f.incl
  if f.excl.isEmpty then out else andO out ((orChain key f.excl).map .not)

/-- Mirrors drone.ToExpr (expr/drone/filter.go) over keys other than `event`: the conjunction in key order. -/
def toE (fs : List (String × Filt)) : Option FE :=
  fs.foldl (fun o kf => andO o (filterE kf.1 kf.2)) none

section Filter

variable (G : String → String → Bool) (env : String → String)

def evalE : FE → Bool
  | .eq k v => env k == v
  | .glob k pat => G pat (env k)
  | .not e => !evalE e
  | .and l r => evalE l && evalE r
  | .or l r => evalE l || evalE r

/-- An absent expression matches every run. -/
def evalAll (o : Option FE) : Bool := o.elim true (evalE G env)

/-- Absent counts as false inside an OR chain. -/
def evalAny (o : Option FE) : Bool := o.elim false (evalE G env)

/-- The rule of the runner: an exclude match rejects; then an empty include list or an include match accepts. -/
def matchF (f : Filt) (v : String) : Bool :=
  if f.excl.any (fun pat => G pat v) then false else f.incl.isEmpty || f.incl.any (fun pat => G pat v)

variable {G env}

theorem valueE_eval (hG : ∀ pat v, isLiteral pat = true → G pat v = (pat == v))
    (htag : ∀ t, t ≠ "" → ((env "tag" == t) = (env "ref" == tagPrefix ++ t))) (key v : String) :
    evalE G env (valueE key v) = G v (env key) := by
  unfold valueE
  by_cases hl : isLiteral v = true
  · simp only [hl, ite_true]
    rw [hG v _ hl]
    cases ht : tagOf v with
    | none => simp [evalE, eq_comm]
    | some t =>
      have hv := tagOf_some ht
      by_cases hc : key = "ref" ∧ t ≠ ""
      · obtain ⟨rfl, hne⟩ := hc
        simp only [evalE, hne, ne_eq, not_false_eq_true, and_self, ite_true]
        rw [htag t hne, ← hv]
        simp [eq_comm]
      · simp only [hc, ite_false, evalE]
        simp [eq_comm]
  · simp [hl, evalE]

theorem orChain_eval (hG : ∀ pat v, isLiteral pat = true → G pat v = (pat == v))
    (htag : ∀ t, t ≠ "" → ((env "tag" == t) = (env "ref" == tagPrefix ++ t))) (key : String) (vs : List String) :
    evalAny G env (orChain key vs) = vs.any (fun pat => G pat (env key)) := by
  have gen : ∀ (o : Option FE) (vs : List String),
      evalAny G env (vs.foldl (fun o v => orO o (some (valueE key v))) o) =
        (evalAny G env o || vs.any (fun pat => G pat (env key))) := by
    intro o vs
    induction vs generalizing o with
    | nil => simp
    | cons a t ih =>
      rw [List.foldl_cons, ih, List.any_cons]
      cases o with
      | none => simp [orO, evalAny, valueE_eval hG htag]
      | some x => simp [orO, evalAny, evalE, valueE_eval hG htag, Bool.or_assoc]
  unfold orChain
  rw [gen]
  simp [evalAny]

theorem orChain_some (key : String) (vs : List String) (h : vs ≠ []) : ∃ x, orChain key vs = some x := by
  have gen : ∀ (y : FE) (vs : List String), ∃ x,
      vs.foldl (fun o v => orO o (some (valueE key v))) (some y) = some x := by
    intro y vs
    induction vs generalizing y with
    | nil => exact ⟨y, rfl⟩
    | cons a t ih => exact ih (FE.or y (valueE key a))
  obtain ⟨a, t, rfl⟩ := List.exists_cons_of_ne_nil h
  exact gen (valueE key a) t

/-- The filter lemma: the expression of `FilterExpr` evaluates to the `Match` result. -/
theorem filterE_eval (hG : ∀ pat v, isLiteral pat = true → G pat v = (pat == v))
    (htag : ∀ t, t ≠ "" → ((env "tag" == t) = (env "ref" == tagPrefix ++ t))) (key : String) (f : Filt) :
    evalAll G env (filterE key f) = matchF G f (env key) := by
  have hi := orChain_eval hG htag key f.incl
  have he := orChain_eval hG htag key f.excl
  unfold filterE matchF
  by_cases hI : f.incl = [] <;> by_cases hE : f.excl = []
  · simp [hI, hE, evalAll]
  · obtain ⟨y, hy⟩ := orChain_some key f.excl hE
    rw [hy] at he
    simp only [evalAny, Option.elim] at he
    generalize f.excl.any (fun pat => G pat (env key)) = a at he ⊢
    subst he
    simp [hI, hE, hy, andO, evalAll, evalE]
  · obtain ⟨x, hx⟩ := orChain_some key f.incl hI
    rw [hx] at hi
    simp only [evalAny, Option.elim] at hi
    generalize f.incl.any (fun pat => G pat (env key)) = b at hi ⊢
    subst hi
    simp [hI, hE, hx, evalAll]
  · obtain ⟨x, hx⟩ := orChain_some key f.incl hI
    obtain ⟨y, hy⟩ := orChain_some key f.excl hE
    rw [hx] at hi
    rw [hy] at he
    simp only [evalAny, Option.elim] at hi he
    generalize f.excl.any (fun pat => G pat (env key)) = a at he ⊢
    generalize f.incl.any (fun pat => G pat (env key)) = b at hi ⊢
    subst he hi
    simp [hI, hE, hx, hy, andO, evalAll, evalE]
    cases evalE G env y <;> cases evalE G env x <;> simp [hI]

/-- `ToExpr` (without the event key and the status call): the conjunction holds exactly when every key matches. -/
theorem toE_eval (hG : ∀ pat v, isLiteral pat = true → G pat v = (pat == v))
    (htag : ∀ t, t ≠ "" → ((env "tag" == t) = (env "ref" == tagPrefix ++ t))) (fs : List (String × Filt)) :
    evalAll G env (toE fs) = fs.all (fun kf => matchF G kf.2 (env kf.1)) := by
  have gen : ∀ (o : Option FE) (fs : List (String × Filt)),
      evalAll G env (fs.foldl (fun o kf => andO o (filterE kf.1 kf.2)) o) =
        (evalAll G env o && fs.all (fun kf => matchF G kf.2 (env kf.1))) := by
    intro o fs
    induction fs generalizing o with
    | nil => simp
    | cons kf t ih =>
      rw [List.foldl_cons, ih, List.all_cons, ← filterE_eval hG htag]
      cases o with
      | none => cases h : filterE kf.1 kf.2 <;> simp [andO, evalAll]
      | some x => cases h : filterE kf.1 kf.2 <;> simp [andO, evalAll, evalE, Bool.and_assoc]
  unfold toE
  rw [gen]
  simp [evalAll]

end Filter

end Adapter

end Drone
end Pipemaster
