import Pipemaster.Core
import Pipemaster.Woodpecker
import Pipemaster.Drone
import Pipemaster.Gitlab
import Pipemaster.Expr

namespace Pipemaster
namespace Tekton

/-! ## The adapter on the core subset

`TDoc` mirrors what the emitter writes for one pipeline (provider/tekton), not YAML syntax: the `Pipeline` document
with its PipelineTasks, the `Task` document of each job, and the filter markers of the EventListener. Each PipelineTask
holds the Task that its `taskRef` names. The Go vector test resolves the reference by name and fails when a Task is
missing or unused. The text fields hold the written strings.

Not in the model, each on purpose:
- Expressions, actions, services, containers, caches, artifacts, outputs, environments, concurrency, matrices, inputs,
  and the `Ext` passthrough: the subset of `Core`.
- The default step image: every step writes `alpine:3.22` and a `step.image` marker (emit_step.go stepImage), which the
  parser removes again. The Go vector test checks both as constants.
- The generated documents: the TriggerBinding, TriggerTemplate, RBAC objects, kustomization index and the
  `# pipemaster:generated` marker. The parser drops them when they equal what it regenerates. The Go vector test
  checks the file set.
- The YAML quoting of every scalar, the block style of a script, the `# pipemaster:id` comment, and the duration
  text of a timeout (`1h30m0s`): codecs that the Go vector test checks on the vector alphabet only.
- Shared-workspace artifacts (`job.needs.artifacts` with a workspace marker). The model refuses a need that
  asks for artifacts (`TekOk`).
- A job that came from a Tekton file: its Ext keys, remote task references and StepAction steps.

The Tekton parser keeps display names, retries and timeouts in the file, so the raw parse differs from the pipeline
only in the runner label. The emitter writes the labels as node selectors of the TriggerTemplate, outside the
model, and the parser does not read them back, so the metadata restores them. -/

section Adapter

open Core

open Woodpecker (Trig trigsOf kindPr kindPush kindTag trigFind)

/-- One written step: its name, `displayName` (empty when absent), script lines and `env`. -/
structure TStep where
  name : String
  disp : String
  script : List String
  env : List Var
  deriving DecidableEq, Repr

/-- One `Task` document. `env` is `stepTemplate.env`, `noop` the `step.script` marker, `needs` the `job.needs`
marker of a finally task. -/
structure TTask where
  name : String
  env : List Var
  steps : List TStep
  noop : Bool
  needs : Option (List Core.Need)
  deriving DecidableEq, Repr

/-- One PipelineTask with the Task it references. `retries` is the `retries` key, `timeout` the `timeout` key in
whole minutes (0 for none), `onError` the `onError: continue` key, `failedOnly` the finally `when` entry that reads
`$(tasks.status)` against `Failed`. -/
structure PTask where
  name : String
  disp : String
  task : TTask
  runAfter : List String
  retries : Option Nat
  timeout : Nat
  onError : Bool
  failedOnly : Bool
  deriving DecidableEq, Repr

/-- One pipeline: `name` is `metadata.name`, `disp` the `spec.displayName`, `trigs` the filter markers of the
EventListener in file order (pull request, push, tag). -/
structure TDoc where
  name : String
  disp : String
  tasks : List PTask
  fin : List PTask
  trigs : List Trig
  deriving DecidableEq, Repr

/-! ### Constants of the emitter and parser -/

/-- Mirrors tekton.noopStepName (provider/tekton/emit_job.go): the name of the placeholder step. -/
def noopName : String := "noop"
/-- Mirrors tekton.noopScript (provider/tekton/emit_job.go): the script of the placeholder step. -/
def noopCmd : String := "true"
/-- Mirrors tekton.maxName (provider/tekton/ids.go): the longest object name, a DNS-1123 label. -/
def maxName : Nat := 63
/-- The longest pipeline ID the fragment takes. The Task name is `<pipeline>-<job>` (emit_plan.go planNames), so
this bound and `maxJobId` keep the name inside `maxName` for every pair, and for the IDs that other adapters give. -/
def maxPipeId : Nat := 20
/-- The longest job ID the fragment takes: `maxPipeId` plus a dash plus this is `maxName`. -/
def maxJobId : Nat := 42

/-! ### Emission -/

/-- The name of the Task of a job (Mirrors tekton.planNames (provider/tekton/emit_plan.go)). -/
def taskName (p : Pipe) (j : Core.Job) : String := p.id ++ "-" ++ j.id

def tstep (s : Core.Step) : TStep := ⟨s.id, s.name, s.script, s.env⟩

/-- The step that stands in for a job with no step (emit_job.go job, `len(b.nodes) == 0`). -/
def noopStep : TStep := ⟨noopName, "", [noopCmd], []⟩

def stepsOf (j : Core.Job) : List TStep := if j.steps.isEmpty then [noopStep] else j.steps.map tstep

/-- Mirrors tekton.taskDoc (provider/tekton/emit_job.go): the Task of a job. The needs of a finally task stay in
a marker, because a finally task has no `runAfter` (emit_pipeline.go runAfter). -/
def taskOf (p : Pipe) (fin : Bool) (j : Core.Job) : TTask :=
  ⟨taskName p j, j.env, stepsOf j, j.steps.isEmpty, if fin && !j.needs.isEmpty then some j.needs else none⟩

/-- Mirrors tekton.pipelineTaskKeys (provider/tekton/emit_pipeline.go): `retries` is the run count minus one and
is absent for one run, `timeout` and `onError` follow the job. A finally job that runs only after a failure
gets the status entry (emit_when.go statusEntry). -/
def ptask (p : Pipe) (fin : Bool) (j : Core.Job) : PTask where
  name := j.id
  disp := j.name
  task := taskOf p fin j
  runAfter := if fin then [] else j.needs.map (·.job)
  retries := if 1 < j.retry then some (j.retry - 1) else none
  timeout := j.timeout
  onError := j.allowFailure
  failedOnly := fin && j.onFailure

/-- Mirrors tekton.Emit (provider/tekton/emit.go) on the core subset: one Pipeline, the Task of each job, and one
filter marker per trigger. -/
def toTekton (p : Pipe) : TDoc :=
  ⟨p.id, p.name, p.jobs.map (ptask p false), p.fin.map (ptask p true), trigsOf p⟩

/-! ### Parsing -/

def parseStepT (s : TStep) : Core.Step := ⟨s.name, s.disp, s.script, s.env⟩

/-- Mirrors tekton.collapseNoop (provider/tekton/collapse.go): the placeholder step goes when the marker names
it. -/
def parseSteps (t : TTask) : List Core.Step :=
  match t.steps with
  | [s] => if t.noop && s.name == noopName && s.script == [noopCmd] then [] else [parseStepT s]
  | ss => ss.map parseStepT

/-- Mirrors tekton.header (provider/tekton/parse_jobs.go) and tekton.collapseFinallyNeeds
(provider/tekton/collapse.go): a finally job reads its needs from the marker, a main job from `runAfter`. -/
def parseJobT (fin : Bool) (t : PTask) : Core.Job where
  id := t.name
  name := t.disp
  needs := if fin then t.task.needs.getD [] else t.runAfter.map fun n => ⟨n, false⟩
  env := t.task.env
  runner := []
  steps := parseSteps t.task
  retry := (t.retries.map (· + 1)).getD 0
  timeout := t.timeout
  manual := false
  allowFailure := t.onError
  onFailure := fin && t.failedOnly

/-- The pipeline Parse builds before the round-trip metadata is applied. -/
def rawTekton (d : TDoc) : Pipe :=
  { id := d.name
    name := d.disp
    push := trigFind kindPush d.trigs
    tag := trigFind kindTag d.trigs
    pr := trigFind kindPr d.trigs
    jobs := d.tasks.map (parseJobT false)
    fin := d.fin.map (parseJobT true) }

/-- The metadata layer of the Tekton adapter: the core layer and the runner labels, which the emitter reports
external or lost (caps.go JobRunner; the parser reads neither, so the metadata keeps them). Retries, timeouts and names stay in the
file, so the layer holds no more. -/
def tektonMeta (p : Pipe) : Meta :=
  { metaOf p with
    runner := fun
      | .job pid jid =>
        if pid = p.id then (findJob p jid).bind fun j => if j.runner = [] then none else some j.runner else none
      | _ => none }

/-- Mirrors tekton.Parse (provider/tekton/parse.go) on the core subset; the round-trip metadata `m` is applied
last (roundtrip.Restore). -/
def fromTekton (d : TDoc) (m : Meta) : Pipe := restore (rawTekton d) m

/-! ### The fragment -/

/-- A DNS-1123 label of lowercase letters, digits and dashes that starts and ends with a letter or digit
(Mirrors dnsid.label (provider/internal/dnsid/dnsid.go)). `SanitizeID` returns such a label unchanged; any other ID is
renamed and keeps its ID in a comment, which the model does not cover. -/
def DnsId (s : String) : Prop :=
  s.toList ≠ [] ∧ (∀ c ∈ s.toList, c.isLower ∨ c.isDigit ∨ c = '-') ∧ (∀ c ∈ s.toList.take 1, c ≠ '-') ∧
    ∀ c ∈ s.toList.reverse.take 1, c ≠ '-'

instance (s : String) : Decidable (DnsId s) := by unfold DnsId; exact inferInstance

/-- A `$(` opens a Tekton variable, which Tekton substitutes with no escape, or a shell command substitution
(text.go parts; emit_text.go checkLiteral). The model refuses every `$(`. -/
def varOpen : List Char → Bool
  | '$' :: '(' :: _ => true
  | _ :: t => varOpen t
  | [] => false

/-- A `${{` opens a template expression in IR text (template.go; text.go template). -/
def tplOpen : List Char → Bool
  | '$' :: '{' :: '{' :: _ => true
  | _ :: t => tplOpen t
  | [] => false

/-- A script line the model keeps: a newline joins lines into one script that the parser splits again; an empty
line at the end is dropped (parse_step.go script trims one trailing newline); a leading `#!` is read as the shell
(parse_step.go script); a line that opens like a guard is read as a condition (`exprtekton.RemoveGuard`). -/
def LineOk (l : String) : Prop :=
  '\n' ∉ l.toList ∧ l ≠ "" ∧ varOpen l.toList = false ∧ tplOpen l.toList = false ∧
    Drone.guardStart.isPrefixOf l.toList = false ∧ ['#', '!'].isPrefixOf l.toList = false

/-- A variable value the codec keeps: no newline, and no `$(` or `${{` (emit_text.go valueNode). -/
def ValueOk (v : String) : Prop := '\n' ∉ v.toList ∧ varOpen v.toList = false ∧ tplOpen v.toList = false

instance (l : String) : Decidable (LineOk l) := by unfold LineOk; exact inferInstance
instance (v : String) : Decidable (ValueOk v) := by unfold ValueOk; exact inferInstance

/-- A trigger pattern for which the emitter writes an exact filter: `globRegex` refuses these characters and writes
a `false` filter with a lost entry instead (emit_trigger.go globRegex). A newline cannot sit in the one-line
marker. -/
def TekPat (s : String) : Prop := ∀ c ∈ s.toList, c ∉ ['[', ']', '{', '}', '!', '\\', '\'', '\n']

instance (s : String) : Decidable (TekPat s) := by unfold TekPat; exact inferInstance

/-- The IR trigger patterns of a pipeline. -/
def trigPats (p : Pipe) : List String := Woodpecker.trigPats p

/-- What one job needs beyond `Wf`. A manual job has no Tekton form: `job.manual` is lost (caps.go JobManual) and the
metadata has no field for it here. One run is written as no `retries` key (emit_pipeline.go pipelineTaskKeys), which
reads back as no retry. A need that asks for artifacts goes through a workspace marker that the model leaves out.
Names are DNS labels that fit `maxName` after the pipeline prefix (emit_plan.go planNames; emit.go dnsName). Script
lines and values pass through unchanged (`LineOk`, `ValueOk`). -/
def TekJob (j : Core.Job) : Prop :=
  (DnsId j.id ∧ j.id.length ≤ maxJobId) ∧ j.manual = false ∧ j.retry ≠ 1 ∧ (∀ n ∈ j.needs, n.artifacts = false) ∧
    (∀ v ∈ j.env, ValueOk v.value) ∧
    ∀ s ∈ j.steps, (DnsId s.id ∧ s.id.length ≤ maxName) ∧ s.script ≠ [] ∧ (∀ l ∈ s.script, LineOk l) ∧
      ∀ v ∈ s.env, ValueOk v.value

instance (j : Core.Job) : Decidable (TekJob j) := by unfold TekJob; exact inferInstance

/-- What Tekton needs beyond `Wf`: a pipeline ID that is a short DNS label, trigger patterns that `globRegex`
accepts, and `TekJob` for every job. -/
def TekOk (p : Pipe) : Prop :=
  (DnsId p.id ∧ p.id.length ≤ maxPipeId) ∧ (∀ x ∈ trigPats p, TekPat x) ∧ ∀ j ∈ p.all, TekJob j

instance (p : Pipe) : Decidable (TekOk p) := by unfold TekOk; exact inferInstance

/-- The Tekton fragment (Mirrors tekton.Caps (provider/tekton/caps.go)): the shared structure plus Tekton's own
needs. -/
def TektonFrag (p : Pipe) : Prop := Wf p ∧ TekOk p

instance (p : Pipe) : Decidable (TektonFrag p) := by unfold TektonFrag; exact inferInstance

/-! ### Theorem 1: parse inverts emit on the Tekton fragment -/

/-- What the Tekton parser keeps of a job: everything but the runner label. -/
def clr (j : Core.Job) : Core.Job := { j with runner := [] }

/-- The pipeline with every runner label dropped. -/
def clearP (p : Pipe) : Pipe := { p with jobs := p.jobs.map clr, fin := p.fin.map clr }

theorem parseStepT_tstep (s : Core.Step) : parseStepT (tstep s) = s := by
  cases s; rfl

theorem parseSteps_of (j : Core.Job) : parseSteps (taskOf p fin j) = j.steps := by
  unfold parseSteps taskOf stepsOf
  cases hs : j.steps with
  | nil => simp [hs, noopStep, noopName, noopCmd]
  | cons a t =>
    cases t with
    | nil => simp [parseStepT_tstep, tstep, parseStepT]
    | cons b t => simp [List.map_map, Function.comp_def, parseStepT_tstep]

theorem retry_rt (r : Nat) (h : r ≠ 1) :
    ((if 1 < r then some (r - 1) else none : Option Nat).map (· + 1)).getD 0 = r := by
  by_cases h1 : 1 < r
  · simp only [h1, if_true, Option.map_some, Option.getD_some]; omega
  · have : r = 0 := by omega
    simp [this]

theorem need_rt (ns : List Core.Need) (h : ∀ n ∈ ns, n.artifacts = false) :
    (ns.map (·.job)).map (fun n => (⟨n, false⟩ : Core.Need)) = ns := by
  induction ns with
  | nil => rfl
  | cons a t ih =>
    simp only [List.map_cons, List.cons.injEq]
    refine ⟨?_, ih fun n hn => h n (by simp [hn])⟩
    have := h a (by simp)
    cases a; simp_all

theorem parse_main (p : Pipe) (j : Core.Job) (hj : TekJob j) (hon : j.onFailure = false) :
    parseJobT false (ptask p false j) = clr j := by
  obtain ⟨_, hm, hr, hn, _, _⟩ := hj
  unfold parseJobT ptask clr
  simp only [parseSteps_of, retry_rt j.retry hr, need_rt j.needs hn, Bool.false_and, hm, hon, Bool.false_eq_true,
    ite_false]
  cases j; simp_all [taskOf]

theorem parse_fin (p : Pipe) (j : Core.Job) (hj : TekJob j) :
    parseJobT true (ptask p true j) = clr j := by
  obtain ⟨_, hm, hr, hn, _, _⟩ := hj
  unfold parseJobT ptask clr
  simp only [parseSteps_of, retry_rt j.retry hr, Bool.true_and, hm, ite_true]
  have hneed : (taskOf p true j).needs.getD [] = j.needs := by
    unfold taskOf
    cases hne : j.needs with
    | nil => simp
    | cons a t => simp
  simp only [hneed]
  cases j; simp_all [taskOf]

theorem rawTekton_toTekton (p : Pipe) (hw : Wf p) (ho : TekOk p) : rawTekton (toTekton p) = clearP p := by
  obtain ⟨_, _, _, _, _, _, _, _, _, hon, _⟩ := hw
  obtain ⟨_, _, hj⟩ := ho
  obtain ⟨h1, h2, h3⟩ := Woodpecker.trigFind_trigsOf p
  unfold rawTekton toTekton clearP
  rw [h1, h2, h3, List.map_map, List.map_map]
  congr 1
  · exact List.map_congr_left fun j hj' => parse_main p j (hj j (by simp [Pipe.all, hj'])) (hon j hj')
  · exact List.map_congr_left fun j hj' => parse_fin p j (hj j (by simp [Pipe.all, hj']))

theorem tektonMeta_fields (p : Pipe) (j : Core.Job) (hf : findJob p j.id = some j) :
    (tektonMeta p).runner (.job p.id j.id) = (if j.runner = [] then none else some j.runner) ∧
    (tektonMeta p).names (.job p.id j.id) = nameOf j.name ∧
    (tektonMeta p).needs (.job p.id j.id) = (if j.needs.any (·.artifacts) = true then some j.needs else none) := by
  refine ⟨?_, ?_, ?_⟩ <;> simp [tektonMeta, metaOf, hf]

theorem restoreJob_tekton (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : ∀ j ∈ p.all, (j.steps.map Core.Step.id).Nodup) (j : Core.Job) (hj : j ∈ p.all)
    (hart : ∀ n ∈ j.needs, n.artifacts = false) :
    restoreJob (tektonMeta p) p.id (clr j) = j := by
  have hf : findJob p j.id = some j := find_id Core.Job.id p.all j hj hn
  obtain ⟨hr, hnm, hnd⟩ := tektonMeta_fields p j hf
  have hany : j.needs.any (·.artifacts) = false := by
    simpa using hart
  have hsteps : j.steps.map (restoreStep (tektonMeta p) p.id j.id) = j.steps :=
    map_self_mem _ _ fun s hsj => by
      have hst : findStep j s.id = some s := find_id Core.Step.id j.steps s hsj (hs j hj)
      unfold restoreStep
      simp [tektonMeta, metaOf, hf, hst, fill_self]
  have hrun : (if j.runner = [] then (none : Option (List String)) else some j.runner).getD [] = j.runner := by
    split <;> simp_all
  unfold restoreJob clr
  simp only [hr, hnm, hnd, hany, fill_self, hsteps, hrun, Bool.false_eq_true, ite_false, Option.getD_none]
  simp [tektonMeta, metaOf_retry, metaOf_timeout]

/-- Theorem 1 of the Tekton adapter: parsing the emitted documents and applying the metadata returns the pipeline,
on the Tekton fragment. -/
theorem tekton_from_to (p : Pipe) (h : TektonFrag p) : fromTekton (toTekton p) (tektonMeta p) = p := by
  obtain ⟨hw, ho⟩ := h
  have hraw := rawTekton_toTekton p hw ho
  obtain ⟨_, hnd, _, _, _, _, _, hsteps, _, _, _⟩ := hw
  obtain ⟨_, _, hj⟩ := ho
  unfold fromTekton
  rw [hraw]
  have hn : (p.all.map Core.Job.id).Nodup := hnd
  have hjs : ∀ j ∈ p.jobs, restoreJob (tektonMeta p) p.id (clr j) = j := fun j hj' =>
    restoreJob_tekton p hn (fun j hj => (hsteps j hj).2) j (by simp [Pipe.all, hj'])
      (hj j (by simp [Pipe.all, hj'])).2.2.2.1
  have hfs : ∀ j ∈ p.fin, restoreJob (tektonMeta p) p.id (clr j) = j := fun j hj' =>
    restoreJob_tekton p hn (fun j hj => (hsteps j hj).2) j (by simp [Pipe.all, hj'])
      (hj j (by simp [Pipe.all, hj'])).2.2.2.1
  have hnm : fill p.name ((tektonMeta p).names (.pipe p.id)) = p.name := by
    simp only [tektonMeta, metaOf, ite_true]; exact fill_self _
  have hid : (tektonMeta p).ids p.id = none := rfl
  unfold restore clearP
  simp only [hid, Option.getD_none, hnm, List.map_map]
  have e1 : List.map (restoreJob (tektonMeta p) p.id ∘ clr) p.jobs = p.jobs :=
    map_self_mem _ p.jobs fun j hj => hjs j hj
  have e2 : List.map (restoreJob (tektonMeta p) p.id ∘ clr) p.fin = p.fin :=
    map_self_mem _ p.fin fun j hj => hfs j hj
  rw [e1, e2]

/-! ### The raw parse keeps the fragment, so a chain can run through Tekton -/

theorem all_clearP (p : Pipe) : (clearP p).all = p.all.map clr := by
  simp [clearP, Pipe.all, List.map_append]

theorem allq (p : Pipe) (P : Core.Job → Prop) (hP : ∀ j, P j → P (clr j)) (h : ∀ j ∈ p.all, P j) :
    ∀ x ∈ (clearP p).all, P x := by
  intro x hx
  rw [all_clearP] at hx
  obtain ⟨j, hj, rfl⟩ := List.mem_map.1 hx
  exact hP j (h j hj)

theorem jobsq (p : Pipe) (P : Core.Job → Prop) (hP : ∀ j, P j → P (clr j)) (h : ∀ j ∈ p.jobs, P j) :
    ∀ x ∈ (clearP p).jobs, P x := by
  intro x hx
  obtain ⟨j, hj, rfl⟩ := List.mem_map.1 (show x ∈ p.jobs.map clr from hx)
  exact hP j (h j hj)

theorem finq (p : Pipe) (P : Core.Job → Prop) (hP : ∀ j, P j → P (clr j)) (h : ∀ j ∈ p.fin, P j) :
    ∀ x ∈ (clearP p).fin, P x := by
  intro x hx
  obtain ⟨j, hj, rfl⟩ := List.mem_map.1 (show x ∈ p.fin.map clr from hx)
  exact hP j (h j hj)

theorem jobIds_clearP_jobs (p : Pipe) : jobIds (clearP p).jobs = jobIds p.jobs := by
  simp [jobIds, clearP, clr, List.map_map, Function.comp_def]

/-- The Tekton parser returns every job with no runner label and keeps the rest, so it keeps every fragment. -/
theorem clearP_preserves (p : Pipe) :
    (Wf p → Wf (clearP p)) ∧ (GhaOk p → GhaOk (clearP p)) ∧ (GitlabOk p → GitlabOk (clearP p)) ∧
      (Woodpecker.WpOk p → Woodpecker.WpOk (clearP p)) ∧ (Drone.DroneOk p → Drone.DroneOk (clearP p)) ∧
      (TekOk p → TekOk (clearP p)) := by
  have hne : p.all ≠ [] → (clearP p).all ≠ [] := by
    intro h h'
    rw [all_clearP, List.map_eq_nil_iff] at h'
    exact h h'
  refine ⟨?_, ?_, ?_, ?_, ?_, ?_⟩
  · intro h
    obtain ⟨h0, h1, h2, h3, h4, h5, h6, h7, h8, h9, h10⟩ := h
    refine ⟨h0, ?_, allq p _ (fun _ h => h) h2, ?_, ?_, ?_, allq p _ (fun _ h => h) h6,
      allq p _ (fun _ h => h) h7, allq p _ (fun _ h => h) h8, jobsq p _ (fun _ h => h) h9,
      finq p _ (fun _ h => h) h10⟩
    · show (jobIds (clearP p).all).Nodup
      have : jobIds (clearP p).all = jobIds p.all := by
        simp [jobIds, all_clearP, clr, List.map_map, Function.comp_def]
      rw [this]; exact h1
    · show (Dag.layers (dagJobs (clearP p).jobs)).isSome = true
      have : dagJobs (clearP p).jobs = dagJobs p.jobs := by
        simp [dagJobs, clearP, clr, List.map_map, Function.comp_def]
      rw [this]; exact h3
    · rw [jobIds_clearP_jobs]
      exact jobsq p (fun j => ∀ n ∈ j.needs, n.job ∈ jobIds p.jobs) (fun _ h => h) h4
    · rw [jobIds_clearP_jobs]
      exact finq p (fun j => ∀ n ∈ j.needs, n.job ∈ jobIds p.jobs) (fun _ h => h) h5
  · intro h
    exact allq p _ (fun _ h => h) h
  · intro h
    obtain ⟨hany, hretry, hfree⟩ := h
    refine ⟨hany, allq p _ (fun _ h => h) hretry, ?_⟩
    intro x hx hnil y hy
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 (show x ∈ p.fin.map clr from hx)
    rw [all_clearP] at hy
    obtain ⟨k, hk, rfl⟩ := List.mem_map.1 hy
    exact hfree j hj hnil k hk
  · intro h
    obtain ⟨hm, hx, hne', hjo⟩ := h
    exact ⟨allq p _ (fun _ h => h) hm, hx, hne hne', allq p _ (fun _ h => h) hjo⟩
  · intro h
    obtain ⟨hne', ht, hx, hjo⟩ := h
    exact ⟨hne hne', ht, hx, allq p _ (fun _ h => h) hjo⟩
  · intro h
    obtain ⟨hid, hx, hjo⟩ := h
    exact ⟨hid, hx, allq p _ (fun _ h => h) hjo⟩

/-- What a raw parse of another adapter keeps for Tekton beyond `Drone.Keep`: the retry runs are the input's or
zero, and a need list without artifacts flags stays without them. -/
def TekKeep (j x : Core.Job) : Prop :=
  (x.retry = 0 ∨ x.retry = j.retry) ∧
    ((∀ n ∈ j.needs, n.artifacts = false) → ∀ n ∈ x.needs, n.artifacts = false)

/-- A pipeline whose jobs keep what `Drone.Keep` and `TekKeep` list keeps the Tekton fragment, when its ID is a short
DNS label. -/
theorem tekOk_of_shape (p : Pipe) {nm : String} (pid : String) (hpid : DnsId pid) (hlen : pid.length ≤ maxPipeId)
    (fm ff : Core.Job → Core.Job) (hm : ∀ j, Drone.Keep j (fm j)) (hf : ∀ j, Drone.Keep j (ff j))
    (km : ∀ j, TekKeep j (fm j)) (kf : ∀ j, TekKeep j (ff j)) (q : Pipe)
    (hq : q = { id := pid, name := nm, push := p.push, tag := p.tag, pr := p.pr,
                jobs := p.jobs.map fm, fin := p.fin.map ff }) :
    TekOk p → TekOk q := by
  intro ho
  obtain ⟨_, hpat, hjob⟩ := ho
  have allm : ∀ x ∈ q.all, ∃ j ∈ p.all, Drone.Keep j x ∧ TekKeep j x := by
    intro x hx
    subst hq
    simp only [Pipe.all, List.mem_append, List.mem_map] at hx ⊢
    rcases hx with ⟨j, hj, rfl⟩ | ⟨j, hj, rfl⟩
    · exact ⟨j, Or.inl hj, hm j, km j⟩
    · exact ⟨j, Or.inr hj, hf j, kf j⟩
  subst hq
  refine ⟨⟨hpid, hlen⟩, hpat, ?_⟩
  intro x hx
  obtain ⟨j, hj, hk, ht⟩ := allm x hx
  obtain ⟨hid, hman, hret, hart, henv, hst⟩ := hjob j hj
  refine ⟨by rw [hk.1]; exact hid, hk.2.2.2.2.1.trans hman, ?_, ht.2 hart, by rw [hk.2.2.1]; exact henv, ?_⟩
  · intro h1
    rcases ht.1 with h0 | h0
    · omega
    · exact hret (h0.symm.trans h1)
  · intro s hs
    rw [hk.2.2.2.1] at hs
    obtain ⟨s0, hs0, e1, e2, e3⟩ := Woodpecker.mem_stripName hs
    obtain ⟨a, b, c, d⟩ := hst s0 hs0
    exact ⟨by rw [e1]; exact a, by rw [e2]; exact b, by rw [e2]; exact c, by rw [e3]; exact d⟩

theorem tekKeep_strip (j : Core.Job) : TekKeep j (Gitlab.strip j) :=
  ⟨Or.inr rfl, fun _ n hn => by
    simp only [Gitlab.strip, List.mem_map] at hn
    obtain ⟨n0, _, rfl⟩ := hn
    rfl⟩

theorem tekKeep_wpMain (j : Core.Job) : TekKeep j (Woodpecker.wpStripMain j) :=
  ⟨Or.inl rfl, fun _ n hn => by
    simp only [Woodpecker.wpStripMain, List.mem_map] at hn
    obtain ⟨n0, _, rfl⟩ := hn
    rfl⟩

theorem tekKeep_wpFin (j : Core.Job) : TekKeep j (Woodpecker.wpStripFin j) := ⟨Or.inl rfl, fun h => h⟩

theorem tekKeep_droneMain (j : Core.Job) : TekKeep j (Drone.droneStripMain j) :=
  ⟨Or.inl rfl, fun _ n hn => by
    simp only [Drone.droneStripMain, List.mem_map] at hn
    obtain ⟨n0, _, rfl⟩ := hn
    rfl⟩

theorem tekKeep_droneFin (j : Core.Job) : TekKeep j (Drone.droneStripFin j) := ⟨Or.inl rfl, fun h => h⟩

end Adapter

/-! ### The `when` decoder

A Tekton `when` entry tests one param against a set of exact strings. The decoder builds a left-nested chain of
equalities joined by `||` for `in`, and of inequalities joined by `&&` for `notin`. The lemma shows that the
chain, read in the GitLab class on the Woodpecker view (a ref the run does not set reads as ""), is Tekton's own
`in` and `notin` test. -/

/-- The operator of a `when` entry. -/
inductive WOp | inOp | notIn
  deriving DecidableEq

/-- Mirrors tekton.decode (expr/tekton/when.go): the expression of an entry that has a ref and at least one value. -/
def decodeWhen (r : Expr.Ref) : WOp → String → List String → Expr.E
  | .inOp, v, vs =>
    vs.foldl (fun acc w => Expr.E.or acc (Expr.E.eq (Expr.E.ref r) (Expr.E.str w)))
      (Expr.E.eq (Expr.E.ref r) (Expr.E.str v))
  | .notIn, v, vs =>
    vs.foldl (fun acc w => Expr.E.and acc (Expr.E.ne (Expr.E.ref r) (Expr.E.str w)))
      (Expr.E.ne (Expr.E.ref r) (Expr.E.str v))

/-- Tekton's own semantics: the param value, or "" when unset, is or is not one of the values. -/
def whenHolds (x : String) : WOp → List String → Bool
  | .inOp, vs => decide (x ∈ vs)
  | .notIn, vs => decide (x ∉ vs)

theorem eval_in_fold (V : Expr.View) (r : Expr.Ref) (x : String) (hx : V r = some x) :
    ∀ (ws : List String) (acc : Expr.E) (b : Bool), Expr.eval .gitlab V acc = .bool b →
      Expr.eval .gitlab V (ws.foldl (fun a w => Expr.E.or a (Expr.E.eq (Expr.E.ref r) (Expr.E.str w))) acc) =
        .bool (b || decide (x ∈ ws)) := by
  intro ws
  induction ws with
  | nil => intro acc b h; simp [h]
  | cons w ws ih =>
    intro acc b h
    simp only [List.foldl_cons]
    have hstep : Expr.eval .gitlab V (Expr.E.or acc (Expr.E.eq (Expr.E.ref r) (Expr.E.str w))) =
        .bool (b || (x == w)) := by
      cases b <;> simp [Expr.eval, h, Expr.logicalTruthy, Expr.rubyTruthy, Expr.refV, hx, Expr.eqV]
    rw [ih _ _ hstep]
    congr 1
    by_cases h1 : x = w <;> by_cases h2 : x ∈ ws <;> simp [h1, h2] <;> cases b <;> simp

theorem eval_notin_fold (V : Expr.View) (r : Expr.Ref) (x : String) (hx : V r = some x) :
    ∀ (ws : List String) (acc : Expr.E) (b : Bool), Expr.eval .gitlab V acc = .bool b →
      Expr.eval .gitlab V (ws.foldl (fun a w => Expr.E.and a (Expr.E.ne (Expr.E.ref r) (Expr.E.str w))) acc) =
        .bool (b && decide (x ∉ ws)) := by
  intro ws
  induction ws with
  | nil => intro acc b h; simp [h]
  | cons w ws ih =>
    intro acc b h
    simp only [List.foldl_cons]
    have hstep : Expr.eval .gitlab V (Expr.E.and acc (Expr.E.ne (Expr.E.ref r) (Expr.E.str w))) =
        .bool (b && !(x == w)) := by
      cases b <;> simp [Expr.eval, h, Expr.logicalTruthy, Expr.rubyTruthy, Expr.refV, hx, Expr.eqV]
    rw [ih _ _ hstep]
    congr 1
    by_cases h1 : x = w <;> by_cases h2 : x ∈ ws <;> simp [h1, h2]

/-- The `when` decoder reads an entry with at least one value as Tekton's `in` or `notin` on the modelled refs:
the decoded expression holds in the GitLab class on the Woodpecker view exactly when the param, or "" if unset,
is or is not one of the values. -/
theorem when_decode (v : Expr.View) (r : Expr.Ref) (op : WOp) (w : String) (ws : List String) :
    Expr.holds .gitlab (Woodpecker.wpView v) (decodeWhen r op w ws) = whenHolds ((v r).getD "") op (w :: ws) := by
  have hx : Woodpecker.wpView v r = some ((v r).getD "") := by simp [Woodpecker.wpView]
  cases op with
  | inOp =>
    have h0 : Expr.eval .gitlab (Woodpecker.wpView v) (Expr.E.eq (Expr.E.ref r) (Expr.E.str w)) =
        .bool ((v r).getD "" == w) := by simp [Expr.eval, Expr.refV, hx, Expr.eqV]
    have := eval_in_fold (Woodpecker.wpView v) r _ hx ws _ _ h0
    simp only [decodeWhen, Expr.holds, this, Expr.truthy, whenHolds]
    by_cases h1 : (v r).getD "" = w <;> simp [h1]
  | notIn =>
    have h0 : Expr.eval .gitlab (Woodpecker.wpView v) (Expr.E.ne (Expr.E.ref r) (Expr.E.str w)) =
        .bool (!((v r).getD "" == w)) := by simp [Expr.eval, Expr.refV, hx, Expr.eqV]
    have := eval_notin_fold (Woodpecker.wpView v) r _ hx ws _ _ h0
    simp only [decodeWhen, Expr.holds, this, Expr.truthy, whenHolds]
    by_cases h1 : (v r).getD "" = w <;> simp [h1]

end Tekton
end Pipemaster
