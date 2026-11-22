import Pipemaster.Core
import Pipemaster.Woodpecker
import Pipemaster.Drone
import Pipemaster.Gitlab
import Pipemaster.Expr
import Pipemaster.Tekton

namespace Pipemaster
namespace Argo

/-! ## The adapter on the core subset

`ADoc` mirrors what the emitter writes for one pipeline (provider/argo), not YAML syntax: the `WorkflowTemplate`
with one DAG task and one `containerSet` template per job, the exit-handler DAG of the finally jobs, and the filter
of each Sensor dependency. A task and the template it names are one `ATask`, because the Go vector test resolves the
reference by name and fails when a template is missing or unused. The text fields hold the written strings.

Not in the model, each on purpose:
- Expressions, actions, services, containers, caches, artifacts, outputs, environments, concurrency, matrices, inputs,
  suspend gates, and the `Ext` passthrough: the subset of `Core`.
- The default image: every container writes `alpine:3.22` and `/workspace` as `workingDir`, with the `workspace`
  `emptyDir` volume. The Go vector test checks these as constants.
- The generated documents: the EventSource, the Sensor triggers (workflow reference, service account, `generateName`),
  the RBAC objects and the Secret reference of the webhook. The Go vector test checks the file set and each constant.
- The YAML quoting of every scalar, the block style of a script, the `# pipemaster:id` comment, the duration text of a
  timeout (`1h30m0s`), the quoted `limit` of a retry, and the order in which `containerSet` containers are listed and
  sorted by their `dependencies` (parse_body.go sortNodes returns a chain in its written order): codecs that the Go
  vector test checks on the vector alphabet only.
- The statements of a `depends` expression other than the plain need: the `Wait` type models what the parser does with
  one (`readWait`), and `depends_bare_not_plain` proves the property that Review Focus 2 asks for. No core job
  carries a status, so the emitter writes `dependencies` only.
- A job that came from an Argo file: its Ext keys, template references and gates.

The Argo parser keeps display names, retries and timeouts in the file, so the raw parse differs from the pipeline
only in the runner label and in the needs of a finally job, which the metadata restores. -/

section Adapter

open Core

open Woodpecker (Trig trigsOf kindPr kindPush kindTag trigFind)

open Tekton (DnsId)

/-- One written container: its name, the display-name marker of its step (empty when absent), the script lines and
`env`. The `env` is the variables of the job followed by the variables of the step. -/
structure AStep where
  name : String
  disp : String
  script : List String
  env : List Var
  deriving DecidableEq, Repr

/-- A task wait, as a DAG task writes it: `dependencies` (a list of task names) or `depends` (an expression). -/
inductive DepExpr
  | atom (task : String) (res : Option String)
  | and (l r : DepExpr)
  | or (l r : DepExpr)
  | not (x : DepExpr)
  deriving DecidableEq, Repr

inductive Wait
  | deps (l : List String)
  | depends (d : DepExpr)
  deriving DecidableEq, Repr

/-- One DAG task with the `containerSet` template that it names. `wait` is the `dependencies` or `depends` key,
`retries` the `retryStrategy.limit`, `timeout` the template `timeout` in whole minutes (0 for none), `onError` the
`continueOn.failed` key, `failedOnly` the `when` of an exit-handler task that reads `workflow.status` against
`Failed`, `noop` the `step.script` marker of a job with no step, and `jobEnv` the variables of the `job.env` marker. -/
structure ATask where
  name : String
  disp : String
  wait : Wait
  retries : Option Nat
  timeout : Nat
  onError : Bool
  failedOnly : Bool
  steps : List AStep
  noop : Bool
  jobEnv : List Var
  deriving DecidableEq, Repr

/-- One pipeline: `name` is `metadata.name`, `disp` the title annotation, `tasks` the entry DAG, `fin` the exit-handler
DAG, `trigs` the Sensor dependencies in IR order (pull request, push, tag). -/
structure ADoc where
  name : String
  disp : String
  tasks : List ATask
  fin : List ATask
  trigs : List Trig
  deriving DecidableEq, Repr

/-! ### Constants of the emitter and parser -/

/-- Mirrors argo.mainStep (provider/argo/parse_body.go): the name of the container that stands in for a job with no
step. -/
def noopName : String := "main"
/-- Mirrors argo.placeholderScript (provider/argo/emit_step.go): the script of that container. -/
def noopCmd : String := "true"
/-- Mirrors argo.maxName (provider/argo/emit.go): the longest object name, a DNS-1123 label. -/
def maxName : Nat := 63
/-- The longest pipeline ID the fragment takes. The Sensor dependency is `<pipeline>-<kind>` (emit_sensor.go depName), and
the other adapters assign IDs of up to nine characters, so the bound is the Tekton one. -/
def maxPipeId : Nat := Tekton.maxPipeId
/-- Mirrors argo.entryTemplate (provider/argo/emit_plan.go) and argo.exitTemplate: the template names that a job
template cannot take (emit_plan.go planNames), so the model refuses job IDs that equal them. -/
def reservedTask : List String := ["main", "exit-handler"]
/-- Mirrors argo.continueTrap (provider/argo/emit_step.go): the first script line that the parser reads as
`continueOnError`. -/
def continueTrap : String := "trap 'exit 0' EXIT # pipemaster:continue-on-error"

/-! ### The wait of a task (Review Focus 2) -/

/-- Mirrors the atoms of argo.parseDepends (provider/argo/parse_depends.go): the task of each atom, in order. -/
def atoms : DepExpr → List String
  | .atom t _ => [t]
  | .and l r => atoms l ++ atoms r
  | .or l r => atoms l ++ atoms r
  | .not x => atoms x

/-- An atom with no result, which Argo also passes when the task is Skipped or Daemoned (parse_depends.go atom). -/
def hasBare : DepExpr → Bool
  | .atom _ r => r.isNone
  | .and l r => hasBare l || hasBare r
  | .or l r => hasBare l || hasBare r
  | .not x => hasBare x

def succeeded : String := "Succeeded"

/-- Mirrors argo.parseDepends (provider/argo/parse_depends.go), field `plain`: only `.Succeeded` atoms joined by `&&`. -/
def dependsPlain : DepExpr → Bool
  | .atom _ r => r == some succeeded
  | .and l r => dependsPlain l && dependsPlain r
  | .or _ _ => false
  | .not _ => false

/-- Mirrors argo.jobBuilder.header and argo.jobBuilder.depends (provider/argo/parse_job.go): `dependencies` names plain
needs, and a `depends` text names its tasks, with a condition unless the text is plain. The pair is the needs and
whether the job gets a condition. -/
def readWait : Wait → List Core.Need × Bool
  | .deps l => (l.map fun n => (⟨n, false⟩ : Core.Need), false)
  | .depends d => ((atoms d).eraseDups.map fun n => (⟨n, false⟩ : Core.Need), !dependsPlain d)

/-- Mirrors argo.emitter.finalDeps (provider/argo/emit_task.go): a need with no status becomes `dependencies`,
never `depends`. -/
def emitWait (ns : List Core.Need) : Wait := .deps (ns.map (·.job))

theorem hasBare_not_plain (d : DepExpr) (h : hasBare d = true) : dependsPlain d = false := by
  induction d with
  | atom t r =>
    cases r with
    | none => simp [dependsPlain]
    | some r => simp [hasBare] at h
  | and l r ihl ihr =>
    simp only [hasBare, Bool.or_eq_true] at h
    rcases h with h | h
    · simp [dependsPlain, ihl h]
    · simp [dependsPlain, ihr h]
  | or l r _ _ => rfl
  | not x _ => rfl

/-- A bare `depends` atom is not read as a plain need: the parser gives the job a condition (Succeeded or Skipped)
for any `depends` expression with a bare atom, in any position. -/
theorem depends_bare_not_plain (d : DepExpr) (h : hasBare d = true) : (readWait (.depends d)).2 = true := by
  simp [readWait, hasBare_not_plain d h]

/-- A plain need emits `dependencies`, and the parser reads it back as a plain need with no condition. -/
theorem argo_depends_plain (ns : List Core.Need) (h : ∀ n ∈ ns, n.artifacts = false) :
    emitWait ns = .deps (ns.map (·.job)) ∧ readWait (emitWait ns) = (ns, false) := by
  refine ⟨rfl, ?_⟩
  simp only [emitWait, readWait]
  rw [Tekton.need_rt ns h]

/-- The explicit form of a plain need, `a.Succeeded && b.Succeeded`, reads as plain, so the bare form is the only one
that the parser has to tell apart. -/
theorem depends_succeeded_plain (a b : String) :
    (readWait (.depends (.and (.atom a (some succeeded)) (.atom b (some succeeded))))).2 = false := by
  simp [readWait, dependsPlain]

/-! ### Emission -/

def astep (je : List Var) (s : Core.Step) : AStep := ⟨s.id, s.name, s.script, je ++ s.env⟩

/-- The container that stands in for a job with no step (emit_job.go containerTemplate, `len(conts) == 0`). It
carries no env: the `job.env` marker holds the job variables. -/
def noopStep : AStep := ⟨noopName, "", [noopCmd], []⟩

def stepsOf (j : Core.Job) : List AStep := if j.steps.isEmpty then [noopStep] else j.steps.map (astep j.env)

/-- Mirrors argo.emitter.job (provider/argo/emit_job.go) and argo.emitter.dagTask (provider/argo/emit_task.go): the
task and the template of one job. A finally task has no `dependencies`: an exit-handler task cannot wait for a task of
the entry DAG (emit_task.go exitDeps), and the needs of a finally job go to the metadata as a lost `job.needs`. -/
def atask (fin : Bool) (j : Core.Job) : ATask where
  name := j.id
  disp := j.name
  wait := emitWait (if fin then [] else j.needs)
  retries := if 0 < j.retry then some (j.retry - 1) else none
  timeout := j.timeout
  onError := j.allowFailure
  failedOnly := fin && j.onFailure
  steps := stepsOf j
  noop := j.steps.isEmpty
  jobEnv := j.env

/-- Mirrors argo.Emit (provider/argo/emit.go) on the core subset: one WorkflowTemplate, and one Sensor dependency per
trigger. -/
def toArgo (p : Pipe) : ADoc :=
  ⟨p.id, p.name, p.jobs.map (atask false), p.fin.map (atask true), trigsOf p⟩

/-! ### Parsing -/

/-- The container env without the variables that the `job.env` marker lifts (parse_marker.go liftEnv). -/
def parseStepA (jn : List String) (c : AStep) : Core.Step :=
  ⟨c.name, c.disp, c.script, c.env.filter fun v => !(jn.contains v.name)⟩

/-- Mirrors argo.jobBuilder.collapseNoop (provider/argo/parse_action.go): the container `main` that runs `true` goes
when the `step.script` marker names it. -/
def parseSteps (t : ATask) : List Core.Step :=
  match t.steps with
  | [s] =>
    if t.noop && s.name == noopName && s.script == [noopCmd] then [] else [parseStepA (varNames t.jobEnv) s]
  | ss => ss.map (parseStepA (varNames t.jobEnv))

/-- Mirrors argo.jobBuilder.build (provider/argo/parse_job.go): a finally job reads `failure()` from the `when`
of its task, and has no needs. -/
def parseJobA (fin : Bool) (t : ATask) : Core.Job where
  id := t.name
  name := t.disp
  needs := (readWait t.wait).1
  env := t.jobEnv
  runner := []
  steps := parseSteps t
  retry := (t.retries.map (· + 1)).getD 0
  timeout := t.timeout
  manual := false
  allowFailure := t.onError
  onFailure := fin && t.failedOnly

/-- The pipeline Parse builds before the round-trip metadata is applied. -/
def rawArgo (d : ADoc) : Pipe :=
  { id := d.name
    name := d.disp
    push := trigFind kindPush d.trigs
    tag := trigFind kindTag d.trigs
    pr := trigFind kindPr d.trigs
    jobs := d.tasks.map (parseJobA false)
    fin := d.fin.map (parseJobA true) }

/-- The metadata layer of the Argo adapter: the core layer, the runner labels (caps.go JobRunner is lost) and the needs
of each finally job (emit_task.go exitDeps reports `job.needs` lost; meta.CollectLost keeps the whole list in
`Lost.Job`). Retries, timeouts and names stay in the file. -/
def argoMeta (p : Pipe) : Meta :=
  { metaOf p with
    runner := fun
      | .job pid jid =>
        if pid = p.id then (findJob p jid).bind fun j => if j.runner = [] then none else some j.runner else none
      | _ => none
    needs := fun
      | .job pid jid =>
        if pid = p.id then
          (p.fin.find? fun j => decide (j.id = jid)).bind fun j => if j.needs.isEmpty then none else some j.needs
        else none
      | _ => none }

/-- Mirrors argo.Parse (provider/argo/parse.go) on the core subset; the round-trip metadata `m` is applied last
(roundtrip.Restore). -/
def fromArgo (d : ADoc) (m : Meta) : Pipe := restore (rawArgo d) m

/-! ### The fragment -/

/-- A text that opens an Argo tag (`{{`). Argo substitutes a tag with a reserved root before the shell runs, and rejects
an unterminated one (emit_text.go tagKinds; text.go). The model refuses every `{{`, which includes `${{`. -/
def tagOpen : List Char → Bool
  | '{' :: '{' :: _ => true
  | _ :: t => tagOpen t
  | [] => false

/-- A script line the model keeps: a newline joins lines into one script that the parser splits again; an empty
line at the end is dropped (parse_body.go shellScript trims one trailing newline); a tag is substituted (`tagOpen`); a
`$(results.` write is rewritten (emitkit result.go NeutralizeResultWriteWhy); a line that opens like a guard is read as
a condition (`exprargo.RemoveGuard`); the trap line is read as `continueOnError` (parse_steps.go collapseSteps). -/
def LineOk (l : String) : Prop :=
  '\n' ∉ l.toList ∧ l ≠ "" ∧ tagOpen l.toList = false ∧ Drone.resultOpen l.toList = false ∧
    Drone.guardStart.isPrefixOf l.toList = false ∧ l ≠ continueTrap

/-- A variable value the codec keeps: no newline, and no tag (emit_text.go valueNode, tagKinds). -/
def ValueOk (v : String) : Prop := '\n' ∉ v.toList ∧ tagOpen v.toList = false

instance (l : String) : Decidable (LineOk l) := by unfold LineOk; exact inferInstance
instance (v : String) : Decidable (ValueOk v) := by unfold ValueOk; exact inferInstance

/-- The characters of a branch or tag name that a regex matches as itself (events.go globLiteral). -/
def litChar (c : Char) : Prop := c.isAlphanum = true ∨ c = '.' ∨ c = '_' ∨ c = '/' ∨ c = '-'

instance (c : Char) : Decidable (litChar c) := by unfold litChar; exact inferInstance

/-- A glob without its trailing `*`. -/
def globBody (l : List Char) : List Char := if l.getLast? = some '*' then l.dropLast else l

/-- A trigger pattern for which the emitter writes an exact regex: a literal name, or a literal prefix and a trailing
`*` (Mirrors argo.globRegex (provider/argo/events.go)). The parser reads those back (parse_sensor.go regexGlob). Any
other glob is lost, and the trigger is not written (emit_sensor.go eventTrigger). -/
def ArgoPat (s : String) : Prop := s ≠ "" ∧ ∀ c ∈ globBody s.toList, litChar c

instance (s : String) : Decidable (ArgoPat s) := by unfold ArgoPat; exact inferInstance

/-- The IR trigger patterns of a pipeline. -/
def trigPats (p : Pipe) : List String := Woodpecker.trigPats p

/-- What one job needs beyond `Wf`. A manual job has no form here: it adds a suspend gate that the model leaves out
(emit_task.go gateTasks). A need
that asks for artifacts goes through task arguments that the model leaves out. Task and container names are DNS labels
that fit `maxName` (emit_plan.go planNames; emit.go dnsName), and a task is not named like the two fixed templates.
Script lines and values pass through unchanged (`LineOk`, `ValueOk`). An env name is not empty (parse_body.go envList),
and a step does not repeat a variable of its job, because the step variable would win (emit_step.go mergeEnv) and the
`job.env` marker would then keep the copy in the step (parse_marker.go liftEnv). -/
def ArgoJob (j : Core.Job) : Prop :=
  (DnsId j.id ∧ j.id.length ≤ maxName ∧ j.id ∉ reservedTask) ∧ j.manual = false ∧
    (∀ n ∈ j.needs, n.artifacts = false) ∧ (∀ v ∈ j.env, ValueOk v.value ∧ v.name ≠ "") ∧
    ∀ s ∈ j.steps, (DnsId s.id ∧ s.id.length ≤ maxName) ∧ s.script ≠ [] ∧ (∀ l ∈ s.script, LineOk l) ∧
      ∀ v ∈ s.env, ValueOk v.value ∧ v.name ≠ "" ∧ v.name ∉ varNames j.env

instance (j : Core.Job) : Decidable (ArgoJob j) := by unfold ArgoJob; exact inferInstance

/-- What Argo needs beyond `Wf`: a pipeline ID that is a short DNS label, trigger patterns that `globRegex` accepts, a
main job (a pipeline with none gets a placeholder task, emit_pipeline.go placeholder), and `ArgoJob` for every job. A tag
trigger with the one pattern `*` is written as the no-filter regex and read back as no filter (parse_sensor.go
buildTriggers), so it is out. -/
def ArgoOk (p : Pipe) : Prop :=
  (DnsId p.id ∧ p.id.length ≤ maxPipeId) ∧ ((∀ x ∈ trigPats p, ArgoPat x) ∧ p.tag ≠ some ["*"]) ∧ p.jobs ≠ [] ∧
    ∀ j ∈ p.all, ArgoJob j

instance (p : Pipe) : Decidable (ArgoOk p) := by unfold ArgoOk; exact inferInstance

/-- The Argo fragment (Mirrors argo.Caps (provider/argo/caps.go)): the shared structure plus Argo's own needs. -/
def ArgoFrag (p : Pipe) : Prop := Wf p ∧ ArgoOk p

instance (p : Pipe) : Decidable (ArgoFrag p) := by unfold ArgoFrag; exact inferInstance

/-! ### Theorem 1: parse inverts emit on the Argo fragment -/

/-- What the Argo parser keeps of a main job: everything but the runner label. -/
def clr (j : Core.Job) : Core.Job := { j with runner := [] }

/-- What the Argo parser keeps of a finally job: it also loses the needs. -/
def clrFin (j : Core.Job) : Core.Job := { j with runner := [], needs := [] }

/-- The pipeline after Argo's raw parse: the runner labels and the needs of the finally jobs are gone. -/
def argClear (p : Pipe) : Pipe := { p with jobs := p.jobs.map clr, fin := p.fin.map clrFin }

theorem env_split (je se : List Var) (h : ∀ v ∈ se, v.name ∉ varNames je) :
    (je ++ se).filter (fun v => !((varNames je).contains v.name)) = se := by
  rw [List.filter_append]
  have h1 : je.filter (fun v => !((varNames je).contains v.name)) = [] := by
    rw [List.filter_eq_nil_iff]
    intro v hv
    have : v.name ∈ varNames je := List.mem_map_of_mem (f := Var.name) hv
    simp [this]
  have h2 : se.filter (fun v => !((varNames je).contains v.name)) = se := by
    rw [List.filter_eq_self]
    intro v hv
    have := h v hv
    simpa using this
  rw [h1, h2, List.nil_append]

theorem parseStepA_astep (j : Core.Job) (s : Core.Step) (h : ∀ v ∈ s.env, v.name ∉ varNames j.env) :
    parseStepA (varNames j.env) (astep j.env s) = s := by
  unfold parseStepA astep
  rw [env_split j.env s.env h]

theorem parseSteps_of (fin : Bool) (j : Core.Job)
    (h : ∀ s ∈ j.steps, ∀ v ∈ s.env, v.name ∉ varNames j.env) : parseSteps (atask fin j) = j.steps := by
  have hmap : (j.steps.map (astep j.env)).map (parseStepA (varNames j.env)) = j.steps := by
    rw [List.map_map]
    exact map_self_mem _ _ fun s hs => by simpa [Function.comp] using parseStepA_astep j s (h s hs)
  by_cases he : j.steps = []
  · simp [parseSteps, atask, stepsOf, he, noopStep, noopName, noopCmd]
  · have hne : j.steps.isEmpty = false := by simpa using he
    unfold parseSteps atask stepsOf
    simp only [hne, Bool.false_eq_true, ite_false]
    generalize hcs : j.steps.map (astep j.env) = cs at hmap ⊢
    rcases cs with _ | ⟨c, _ | ⟨d, t⟩⟩
    · exact absurd hmap.symm he
    · simp only [Bool.false_and, Bool.false_eq_true, ite_false]
      simpa using hmap
    · simpa using hmap

theorem retry_rt (n : Nat) :
    ((if 0 < n then some (n - 1) else none : Option Nat).map (· + 1)).getD 0 = n := by
  by_cases h : 0 < n <;> simp [h] <;> omega

theorem parse_main (j : Core.Job) (hj : ArgoJob j) (hon : j.onFailure = false) :
    parseJobA false (atask false j) = clr j := by
  obtain ⟨_, hm, hn, _, hst⟩ := hj
  have hdisj : ∀ s ∈ j.steps, ∀ v ∈ s.env, v.name ∉ varNames j.env := fun s hs v hv => ((hst s hs).2.2.2 v hv).2.2
  have hw := (argo_depends_plain j.needs hn).2
  have hps := parseSteps_of false j hdisj
  unfold parseJobA
  rw [hps]
  unfold atask clr
  simp only [retry_rt j.retry, hw, hm, hon, Bool.false_and, Bool.false_eq_true, ite_false]

theorem parse_fin (j : Core.Job) (hj : ArgoJob j) : parseJobA true (atask true j) = clrFin j := by
  obtain ⟨_, hm, _, _, hst⟩ := hj
  have hdisj : ∀ s ∈ j.steps, ∀ v ∈ s.env, v.name ∉ varNames j.env := fun s hs v hv => ((hst s hs).2.2.2 v hv).2.2
  have hps := parseSteps_of true j hdisj
  unfold parseJobA
  rw [hps]
  unfold atask clrFin
  simp only [retry_rt j.retry, hm, Bool.true_and, ite_true]
  cases j; simp_all [emitWait, readWait]

theorem rawArgo_toArgo (p : Pipe) (hw : Wf p) (ho : ArgoOk p) : rawArgo (toArgo p) = argClear p := by
  obtain ⟨_, _, _, _, _, _, _, _, _, hon, _⟩ := hw
  obtain ⟨_, _, _, hj⟩ := ho
  obtain ⟨h1, h2, h3⟩ := Woodpecker.trigFind_trigsOf p
  unfold rawArgo toArgo argClear
  rw [h1, h2, h3, List.map_map, List.map_map]
  congr 1
  · exact List.map_congr_left fun j hj' => parse_main j (hj j (by simp [Pipe.all, hj'])) (hon j hj')
  · exact List.map_congr_left fun j hj' => parse_fin j (hj j (by simp [Pipe.all, hj']))

theorem argoMeta_fields (p : Pipe) (j : Core.Job) (hf : findJob p j.id = some j) :
    (argoMeta p).runner (.job p.id j.id) = (if j.runner = [] then none else some j.runner) ∧
    (argoMeta p).names (.job p.id j.id) = nameOf j.name := by
  refine ⟨?_, ?_⟩ <;> simp [argoMeta, metaOf, hf]

theorem fin_find (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup) (j : Core.Job) (hj : j ∈ p.fin) :
    p.fin.find? (fun x => decide (x.id = j.id)) = some j := by
  have hfn : (p.fin.map Core.Job.id).Nodup := by
    have := hn
    simp only [Pipe.all, List.map_append, List.nodup_append] at this
    exact this.2.1
  exact find_id Core.Job.id p.fin j hj hfn

theorem fin_find_none (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup) (j : Core.Job) (hj : j ∈ p.jobs) :
    p.fin.find? (fun x => decide (x.id = j.id)) = none := by
  rw [List.find?_eq_none]
  intro x hx hxe
  have := hn
  simp only [Pipe.all, List.map_append, List.nodup_append] at this
  have hne := this.2.2 j.id (List.mem_map_of_mem (f := Core.Job.id) hj) x.id (List.mem_map_of_mem (f := Core.Job.id) hx)
  have hx2 : x.id = j.id := by simpa using hxe
  exact hne hx2.symm

theorem restoreJob_common (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : ∀ j ∈ p.all, (j.steps.map Core.Step.id).Nodup) (j : Core.Job) (hj : j ∈ p.all) :
    j.steps.map (restoreStep (argoMeta p) p.id j.id) = j.steps := by
  have hf : findJob p j.id = some j := find_id Core.Job.id p.all j hj hn
  exact map_self_mem _ _ fun s hsj => by
    have hst : findStep j s.id = some s := find_id Core.Step.id j.steps s hsj (hs j hj)
    unfold restoreStep
    simp [argoMeta, metaOf, hf, hst, fill_self]

theorem restoreJob_main (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : ∀ j ∈ p.all, (j.steps.map Core.Step.id).Nodup) (j : Core.Job) (hj : j ∈ p.jobs) :
    restoreJob (argoMeta p) p.id (clr j) = j := by
  have hja : j ∈ p.all := by simp [Pipe.all, hj]
  have hf : findJob p j.id = some j := find_id Core.Job.id p.all j hja hn
  obtain ⟨hr, hnm⟩ := argoMeta_fields p j hf
  have hnone : (argoMeta p).needs (.job p.id j.id) = none := by
    simp [argoMeta, fin_find_none p hn j hj]
  have hsteps := restoreJob_common p hn hs j hja
  have hrun : (if j.runner = [] then (none : Option (List String)) else some j.runner).getD [] = j.runner := by
    split <;> simp_all
  unfold restoreJob clr
  simp only [hr, hnm, hnone, fill_self, hsteps, hrun, Option.getD_none]
  simp [argoMeta, metaOf_retry, metaOf_timeout]

theorem restoreJob_fin (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : ∀ j ∈ p.all, (j.steps.map Core.Step.id).Nodup) (j : Core.Job) (hj : j ∈ p.fin) :
    restoreJob (argoMeta p) p.id (clrFin j) = j := by
  have hja : j ∈ p.all := by simp [Pipe.all, hj]
  have hf : findJob p j.id = some j := find_id Core.Job.id p.all j hja hn
  obtain ⟨hr, hnm⟩ := argoMeta_fields p j hf
  have hneed : (argoMeta p).needs (.job p.id j.id) = (if j.needs.isEmpty then none else some j.needs) := by
    simp [argoMeta, fin_find p hn j hj]
  have hsteps := restoreJob_common p hn hs j hja
  have hrun : (if j.runner = [] then (none : Option (List String)) else some j.runner).getD [] = j.runner := by
    split <;> simp_all
  have hnd : ((if j.needs.isEmpty then none else some j.needs : Option (List Core.Need))).getD [] = j.needs := by
    cases hne : j.needs with
    | nil => simp
    | cons a t => simp
  unfold restoreJob clrFin
  simp only [hr, hnm, hneed, fill_self, hsteps, hrun, hnd]
  simp [argoMeta, metaOf_retry, metaOf_timeout]

/-- Theorem 1 of the Argo adapter: parsing the emitted documents and applying the metadata returns the pipeline, on
the Argo fragment. Proved about the model. -/
theorem argo_from_to (p : Pipe) (h : ArgoFrag p) : fromArgo (toArgo p) (argoMeta p) = p := by
  obtain ⟨hw, ho⟩ := h
  have hraw := rawArgo_toArgo p hw ho
  obtain ⟨_, hnd, _, _, _, _, _, hsteps, _, _, _⟩ := hw
  unfold fromArgo
  rw [hraw]
  have hn : (p.all.map Core.Job.id).Nodup := hnd
  have hst : ∀ j ∈ p.all, (j.steps.map Core.Step.id).Nodup := fun j hj => (hsteps j hj).2
  have hnm : fill p.name ((argoMeta p).names (.pipe p.id)) = p.name := by
    simp only [argoMeta, metaOf, ite_true]; exact fill_self _
  have hid : (argoMeta p).ids p.id = none := rfl
  unfold restore argClear
  simp only [hid, Option.getD_none, hnm, List.map_map]
  have e1 : List.map (restoreJob (argoMeta p) p.id ∘ clr) p.jobs = p.jobs :=
    map_self_mem _ p.jobs fun j hj => restoreJob_main p hn hst j hj
  have e2 : List.map (restoreJob (argoMeta p) p.id ∘ clrFin) p.fin = p.fin :=
    map_self_mem _ p.fin fun j hj => restoreJob_fin p hn hst j hj
  rw [e1, e2]

/-! ### The raw parse keeps the fragment, so a chain can run through Argo

The raw parse loses the needs of every finally job (the metadata restores them) besides the runner labels. A finally
job that kept no need would let GitLab put the stage needs on it, so the raw pipeline leaves the GitLab fragment when a
main job has needs. `FinDrop` is the one condition that rules this out for a chain. -/

/-- A finally job with no needs. -/
def ef (j : Core.Job) : Core.Job := { j with needs := [] }

/-- The pipeline with the needs of every finally job dropped. -/
def emptyFin (p : Pipe) : Pipe := { p with fin := p.fin.map ef }

/-- Some finally job has needs only if no main job has any. After the Argo parse the finally jobs have none, and
GitLab then reads the finally job as waiting for its stage, which only the same-shape pipelines keep
(`GitlabOk`, `noNeeds`). -/
def FinDrop (p : Pipe) : Prop := (∃ f ∈ p.fin, f.needs ≠ []) → ∀ j ∈ p.jobs, j.needs = []

instance (p : Pipe) : Decidable (FinDrop p) := by unfold FinDrop; exact inferInstance

theorem argClear_eq (p : Pipe) : argClear p = Tekton.clearP (emptyFin p) := by
  simp [argClear, Tekton.clearP, emptyFin, clrFin, Tekton.clr, clr, ef, List.map_map, Function.comp_def]

theorem allef (p : Pipe) (P : Core.Job → Prop) (hP : ∀ j, P j → P (ef j)) (h : ∀ j ∈ p.all, P j) :
    ∀ x ∈ (emptyFin p).all, P x := by
  intro x hx
  simp only [Pipe.all, emptyFin, List.mem_append, List.mem_map] at hx
  rcases hx with hx | ⟨j, hj, rfl⟩
  · exact h x (by simp [Pipe.all, hx])
  · exact hP j (h j (by simp [Pipe.all, hj]))

theorem jobIds_emptyFin (p : Pipe) : jobIds (emptyFin p).all = jobIds p.all := by
  simp [jobIds, emptyFin, ef, Pipe.all, List.map_append, List.map_map, Function.comp_def]

theorem emptyFin_ne (p : Pipe) (h : p.all ≠ []) : (emptyFin p).all ≠ [] := by
  intro h'
  apply h
  simpa [Pipe.all, emptyFin] using h'

theorem emptyFin_preserves (p : Pipe) (hd : FinDrop p) :
    (Wf p → Wf (emptyFin p)) ∧ (GhaOk p → GhaOk (emptyFin p)) ∧ (GitlabOk p → GitlabOk (emptyFin p)) ∧
      (Woodpecker.WpOk p → Woodpecker.WpOk (emptyFin p)) ∧ (Drone.DroneOk p → Drone.DroneOk (emptyFin p)) ∧
      (Tekton.TekOk p → Tekton.TekOk (emptyFin p)) ∧ (ArgoOk p → ArgoOk (emptyFin p)) := by
  refine ⟨?_, ?_, ?_, ?_, ?_, ?_, ?_⟩
  · intro h
    obtain ⟨h0, h1, h2, h3, h4, h5, h6, h7, h8, h9, h10⟩ := h
    refine ⟨h0, ?_, allef p _ (fun _ h => h) h2, h3, h4, ?_, ?_, allef p _ (fun _ h => h) h7,
      allef p _ (fun _ h => h) h8, h9, ?_⟩
    · show (jobIds (emptyFin p).all).Nodup
      rw [jobIds_emptyFin]; exact h1
    · intro j hj n hn
      obtain ⟨j0, _, rfl⟩ := List.mem_map.1 hj
      simp [ef] at hn
    · exact allef p (fun j => (j.needs.map (·.job)).Nodup) (fun j _ => by simp [ef]) h6
    · intro j hj
      obtain ⟨j0, hj0, rfl⟩ := List.mem_map.1 hj
      simpa [ef] using h10 j0 hj0
  · intro h
    exact allef p _ (fun _ h => h) h
  · intro h
    obtain ⟨hany, hretry, hfree⟩ := h
    refine ⟨hany, allef p _ (fun _ h => h) hretry, ?_⟩
    intro x hx _ y hy
    obtain ⟨x0, hx0, rfl⟩ := List.mem_map.1 hx
    simp only [Pipe.all, emptyFin, List.mem_append, List.mem_map] at hy
    rcases hy with hy | ⟨y0, hy0, rfl⟩
    · by_cases hex : ∃ f ∈ p.fin, f.needs ≠ []
      · exact hd hex y hy
      · push Not at hex
        exact hfree x0 hx0 (hex x0 hx0) y (by simp [Pipe.all, hy])
    · rfl
  · intro h
    obtain ⟨hm, hx, hne, hjo⟩ := h
    exact ⟨allef p _ (fun _ h => h) hm, hx, emptyFin_ne p hne, allef p _ (fun _ h => h) hjo⟩
  · intro h
    obtain ⟨hne, ht, hx, hjo⟩ := h
    exact ⟨emptyFin_ne p hne, ht, hx, allef p _ (fun _ h => h) hjo⟩
  · intro h
    obtain ⟨hid, hx, hjo⟩ := h
    refine ⟨hid, hx, allef p _ (fun j hj => ?_) hjo⟩
    obtain ⟨a, b, c, _, e, f⟩ := hj
    exact ⟨a, b, c, fun n hn => by simp [ef] at hn, e, f⟩
  · intro h
    obtain ⟨hid, hx, hne, hjo⟩ := h
    refine ⟨hid, hx, hne, allef p _ (fun j hj => ?_) hjo⟩
    obtain ⟨a, b, _, e, f⟩ := hj
    exact ⟨a, b, fun n hn => by simp [ef] at hn, e, f⟩

theorem argoOk_clearP (q : Pipe) : ArgoOk q → ArgoOk (Tekton.clearP q) := by
  intro h
  obtain ⟨hid, hx, hne, hjo⟩ := h
  refine ⟨hid, hx, ?_, Tekton.allq q _ (fun _ h => h) hjo⟩
  simpa [Tekton.clearP] using hne

/-- The Argo parser keeps every fragment, when no finally job needs another job and a main job at once. -/
theorem argClear_preserves (p : Pipe) (hd : FinDrop p) :
    (Wf p → Wf (argClear p)) ∧ (GhaOk p → GhaOk (argClear p)) ∧ (GitlabOk p → GitlabOk (argClear p)) ∧
      (Woodpecker.WpOk p → Woodpecker.WpOk (argClear p)) ∧ (Drone.DroneOk p → Drone.DroneOk (argClear p)) ∧
      (Tekton.TekOk p → Tekton.TekOk (argClear p)) ∧ (ArgoOk p → ArgoOk (argClear p)) ∧ FinDrop (argClear p) := by
  have he := emptyFin_preserves p hd
  have hc := Tekton.clearP_preserves (emptyFin p)
  rw [argClear_eq]
  refine ⟨fun h => hc.1 (he.1 h), fun h => hc.2.1 (he.2.1 h), fun h => hc.2.2.1 (he.2.2.1 h),
    fun h => hc.2.2.2.1 (he.2.2.2.1 h), fun h => hc.2.2.2.2.1 (he.2.2.2.2.1 h),
    fun h => hc.2.2.2.2.2 (he.2.2.2.2.2.1 h), fun h => argoOk_clearP _ (he.2.2.2.2.2.2 h), ?_⟩
  rintro ⟨f, hf, hne⟩
  obtain ⟨f1, hf1, rfl⟩ := List.mem_map.1 (show f ∈ (emptyFin p).fin.map Tekton.clr from hf)
  obtain ⟨g, _, rfl⟩ := List.mem_map.1 (show f1 ∈ p.fin.map ef from hf1)
  exact absurd rfl hne

theorem finDrop_clearP (q : Pipe) : FinDrop q → FinDrop (Tekton.clearP q) := by
  rintro hd ⟨f, hf, hne⟩
  obtain ⟨f0, hf0, rfl⟩ := List.mem_map.1 (show f ∈ q.fin.map Tekton.clr from hf)
  intro j hj
  obtain ⟨j0, hj0, rfl⟩ := List.mem_map.1 (show j ∈ q.jobs.map Tekton.clr from hj)
  exact hd ⟨f0, hf0, hne⟩ j0 hj0

/-- A pipeline whose jobs keep what `Drone.Keep` and `Tekton.TekKeep` list keeps the Argo fragment, when its ID is a
short DNS label. -/
theorem argoOk_of_shape (p : Pipe) (pid : String) (hpid : DnsId pid) (hlen : pid.length ≤ maxPipeId)
    (fm ff : Core.Job → Core.Job) (hm : ∀ j, Drone.Keep j (fm j)) (hf : ∀ j, Drone.Keep j (ff j))
    (km : ∀ j, Tekton.TekKeep j (fm j)) (kf : ∀ j, Tekton.TekKeep j (ff j)) (q : Pipe)
    (hq : q = { id := pid, name := "", push := p.push, tag := p.tag, pr := p.pr,
                jobs := p.jobs.map fm, fin := p.fin.map ff }) :
    ArgoOk p → ArgoOk q := by
  intro ho
  obtain ⟨_, hpat, hne, hjob⟩ := ho
  have allm : ∀ x ∈ q.all, ∃ j ∈ p.all, Drone.Keep j x ∧ Tekton.TekKeep j x := by
    intro x hx
    subst hq
    simp only [Pipe.all, List.mem_append, List.mem_map] at hx ⊢
    rcases hx with ⟨j, hj, rfl⟩ | ⟨j, hj, rfl⟩
    · exact ⟨j, Or.inl hj, hm j, km j⟩
    · exact ⟨j, Or.inr hj, hf j, kf j⟩
  subst hq
  refine ⟨⟨hpid, hlen⟩, hpat, by simpa using hne, ?_⟩
  intro x hx
  obtain ⟨j, hj, hk, ht⟩ := allm x hx
  obtain ⟨⟨hid, hl, hres⟩, hman, hart, henv, hst⟩ := hjob j hj
  refine ⟨⟨by rw [hk.1]; exact hid, by rw [hk.1]; exact hl, by rw [hk.1]; exact hres⟩,
    hk.2.2.2.2.1.trans hman, ht.2 hart, by rw [hk.2.2.1]; exact henv, ?_⟩
  · intro s hs
    rw [hk.2.2.2.1] at hs
    obtain ⟨s0, hs0, e1, e2, e3⟩ := Woodpecker.mem_stripName hs
    obtain ⟨a, b, c, d⟩ := hst s0 hs0
    refine ⟨by rw [e1]; exact a, by rw [e2]; exact b, by rw [e2]; exact c, ?_⟩
    intro v hv
    rw [e3] at hv
    obtain ⟨d1, d2, d3⟩ := d v hv
    exact ⟨d1, d2, by rw [hk.2.2.1]; exact d3⟩

/-- The same shape keeps `FinDrop`, because a raw parse that keeps the need lists empty or full keeps their emptiness
(`Drone.Keep`). -/
theorem finDrop_of_shape (p : Pipe) (pid : String) (fm ff : Core.Job → Core.Job)
    (hm : ∀ j, Drone.Keep j (fm j)) (hf : ∀ j, Drone.Keep j (ff j)) (q : Pipe)
    (hq : q = { id := pid, name := "", push := p.push, tag := p.tag, pr := p.pr,
                jobs := p.jobs.map fm, fin := p.fin.map ff }) :
    FinDrop p → FinDrop q := by
  intro hd
  subst hq
  rintro ⟨f, hfm, hne⟩
  obtain ⟨f0, hf0, rfl⟩ := List.mem_map.1 hfm
  have h0 : f0.needs ≠ [] := fun h => hne ((hf f0).2.2.2.2.2.2.1.2 h)
  have hdr := hd ⟨f0, hf0, h0⟩
  intro j hj
  obtain ⟨j0, hj0, rfl⟩ := List.mem_map.1 hj
  exact (hm j0).2.2.2.2.2.2.1.2 (hdr j0 hj0)

end Adapter

/-! ### The `when` decoder

An Argo `when` is one expr-lang text `{{= ... }}`. The decoder reads `==` and `!=` between a ref and a string literal, the
literals `true` and `false`, `!`, `&&` and `||`, with groups (expr/argo/when.go parse). `AW` is the tree it builds from
those tokens. The lemma shows that the tree, read in the GitLab class on the Woodpecker view (a workflow parameter
the run does not set reads as ""), has the Boolean value that expr-lang gives the text. expr-lang is strictly typed, so
each operand of `&&`, `||` and `!` is a Boolean and each comparison is between two strings. -/

/-- The tree of a `when` text in the modelled subset. -/
inductive AW
  | bool (b : Bool)
  | eq (r : Expr.Ref) (s : String)
  | ne (r : Expr.Ref) (s : String)
  | not (w : AW)
  | and (l r : AW)
  | or (l r : AW)

/-- Mirrors argo.parse (expr/argo/when.go): the AST of the shared expression type that the decoder returns. -/
def decodeW : AW → Expr.E
  | .bool b => .bool b
  | .eq r s => .eq (.ref r) (.str s)
  | .ne r s => .ne (.ref r) (.str s)
  | .not w => .not (decodeW w)
  | .and l r => .and (decodeW l) (decodeW r)
  | .or l r => .or (decodeW l) (decodeW r)

/-- expr-lang's own semantics on the subset: a parameter that is unset is "", `==` and `!=` compare strings, and the
logical operators are the Boolean ones. -/
def exprLang (v : Expr.View) : AW → Bool
  | .bool b => b
  | .eq r s => decide (((v r).getD "") = s)
  | .ne r s => decide (((v r).getD "") ≠ s)
  | .not w => !exprLang v w
  | .and l r => exprLang v l && exprLang v r
  | .or l r => exprLang v l || exprLang v r

theorem eval_decodeW (v : Expr.View) (w : AW) :
    Expr.eval .gitlab (Woodpecker.wpView v) (decodeW w) = .bool (exprLang v w) := by
  induction w with
  | bool b => rfl
  | eq r s => by_cases h : (v r).getD "" = s <;> simp [decodeW, exprLang, Expr.eval, Expr.refV, Woodpecker.wpView, Expr.eqV, h]
  | ne r s => by_cases h : (v r).getD "" = s <;> simp [decodeW, exprLang, Expr.eval, Expr.refV, Woodpecker.wpView, Expr.eqV, h]
  | not w ih => simp [decodeW, exprLang, Expr.eval, ih, Expr.truthy]
  | and l r ihl ihr =>
    cases hl : exprLang v l <;> simp [decodeW, exprLang, Expr.eval, ihl, ihr, hl, Expr.logicalTruthy, Expr.rubyTruthy]
  | or l r ihl ihr =>
    cases hl : exprLang v l <;> simp [decodeW, exprLang, Expr.eval, ihl, ihr, hl, Expr.logicalTruthy, Expr.rubyTruthy]

/-- The `when` decoder reads a text of the modelled subset as expr-lang does: the decoded expression holds in the GitLab
class on the Woodpecker view exactly when expr-lang evaluates the text to true. Proved about the model. -/
theorem argo_when_decode (v : Expr.View) (w : AW) :
    Expr.holds .gitlab (Woodpecker.wpView v) (decodeW w) = exprLang v w := by
  simp [Expr.holds, eval_decodeW, Expr.truthy]

end Argo
end Pipemaster
