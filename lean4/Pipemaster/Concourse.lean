import Std.Data.String.ToNat
import Pipemaster.Core
import Pipemaster.Woodpecker
import Pipemaster.Drone
import Pipemaster.Tekton
import Pipemaster.Argo

namespace Pipemaster
namespace Concourse

/-! ## The adapter on the core subset

`CDoc` mirrors what the emitter writes for one pipeline (provider/concourse), not YAML syntax: one file, one job per
IR job, and the markers that the parser reads back. When another provider wrote the IR, the emitter writes a carry
marker for the pipeline, for each job and for each task (emit_carry.go carry): the IR fields that the file cannot say,
as YAML, beside a digest of the text they were written next to. The parser applies a carry only when the digest still
matches (parse_marker.go carryOf), so a carry always applies to an unedited file, which is the case here. The model
holds each carry as the IR value it decodes to.

What the parser takes from the file and what from the carry (parse_marker.go carryJob, carryStep, carryPipeline):
- File: the pipeline ID (the file name), each job name, each need (the `passed` list), each script that the args can say, the task names and
  the ensure hook of the sink job.
- Carry: the pipeline name, the triggers and the finally jobs (carryPipeline); for each job every field but its ID,
  name, steps and need jobs (carryJob); for each task its ID, name and env, and its script when the args cannot say it (carryStep).
- Display names: a job name comes from its `display.name` marker, so `disp` holds it.

Not in the model, each on purpose:
- Expressions, actions, services, containers, caches, artifacts, outputs, environments, concurrency, matrices, inputs,
  and the `Ext` passthrough: the subset of `Core`.
- Written but overridden by a carry on the raw parse: the resource definitions and the trigger flags of the gets, the
  `do` wrapper with `attempts` and `timeout`, `try` for `allow_failure`, the worker `tags`, the task `params`, and the
  `config` of each task (platform, image, inputs, outputs, `run`). The Go vector test decodes the file and checks
  these as constants or ignores them, so they are a codec checked on the vector alphabet only.
- A job or task name that the emitter must change: SanitizeID lower-cases and `unique` appends `-2` (emit_job.go jobs,
  emit_step.go step). The model refuses IDs that need a change (`CcId`, `StepOk`) except the one clash that the
  finally steps cause, which `uniqFrom` models.
- `ir.Normalize` (jobs and finally jobs sorted by ID, needs and runner labels sorted): the Go parser applies it, as
  every adapter's parser does, and the theorems hold over normalised pipelines (see `Core.Wf`).
- A job with no step still gets the shared source `get` (emit_gets.go gets), so a need on it orders the jobs.
- A pipeline with no job: the emitter writes a placeholder job (emit_pipeline.go placeholder) and the parser drops it.
- The digest: a carry applies when the digest matches, and the model has no edited file. -/

section Adapter

open Core

/-- One written task: `name` is the task name in the file, `script` the lines that the parser reads from `run.args`,
`carried` the script lines of the step carry (empty when the carry holds none), and the other three fields are the
step carry (ID, display name and env). -/
structure CTask where
  name : String
  script : List String
  carried : List String
  cid : String
  cname : String
  cenv : List Var
  deriving DecidableEq, Repr

/-- One job of the file: its name, the display-name marker (empty when absent), the `passed` list of the source `get`,
the tasks of the plan, the tasks of the `ensure` hook, and the job carry (every job field but the steps, which the
carry holds with the full needs list). -/
structure CJob where
  name : String
  disp : String
  passed : List String
  body : List CTask
  hook : List CTask
  carry : Core.Job
  deriving DecidableEq, Repr

/-- One pipeline file: `file` is the base name (the pipeline ID), `carry` the pipeline carry (name, triggers and
finally jobs, with no jobs), and `finJob` and `finTasks` the `pipeline.finally` marker: the job that holds the finally
steps as hook tasks, and the task names (empty when no hook). -/
structure CDoc where
  file : String
  carry : Core.Pipe
  jobs : List CJob
  finJob : String
  finTasks : List String
  deriving DecidableEq, Repr

/-! ### The task names of the finally steps -/

/-- The candidate name after `i` clashes: the suffix counts from `-2`. -/
def suffixName (b : String) (i : Nat) : String := b ++ "-" ++ (i + 2).repr

/-- Mirrors concourse.unique (provider/concourse/emit.go): the name, or the name with the first free suffix from `-2`.
Among the first `taken.length + 2` candidates one is free, so the search stops there. -/
def uniqName (taken : List String) (base : String) : String :=
  ((base :: (List.range (taken.length + 1)).map (suffixName base)).find? fun n => decide (n ∉ taken)).getD base

/-- The names that `uniqName` gives a list of bases in order, each avoiding `acc` and the names given before. -/
def uniqFrom : List String → List String → List String
  | _, [] => []
  | acc, b :: bs => uniqName acc b :: uniqFrom (acc ++ [uniqName acc b]) bs

theorem suffixName_inj (b : String) : Function.Injective (suffixName b) := by
  intro i k h
  have h1 : (b ++ "-" ++ (i + 2).repr).toList = (b ++ "-" ++ (k + 2).repr).toList := by
    simpa [suffixName] using congrArg String.toList h
  simp only [String.toList_append] at h1
  have h2 := List.append_cancel_left h1
  have h3 : (i + 2).repr = (k + 2).repr := String.toList_injective h2
  have := Nat.repr_injective h3
  omega

theorem suffixName_ne (b : String) (i : Nat) : suffixName b i ≠ b := by
  intro h
  have h1 : (suffixName b i).toList.length = b.toList.length := by rw [h]
  simp [suffixName, String.toList_append] at h1

theorem uniqName_fresh (taken : List String) (base : String) : uniqName taken base ∉ taken := by
  unfold uniqName
  set cs : List String := base :: (List.range (taken.length + 1)).map (suffixName base) with hcs
  have hnd : cs.Nodup := by
    refine List.nodup_cons.2 ⟨?_, (List.nodup_range).map (suffixName_inj base)⟩
    intro hm
    obtain ⟨i, _, hi⟩ := List.mem_map.1 hm
    exact suffixName_ne base i hi
  have hlen : cs.length = taken.length + 2 := by simp [hcs]
  have hsome : ∃ c ∈ cs, c ∉ taken := by
    by_contra hall
    push Not at hall
    have hsub : cs ⊆ taken := fun c hc => hall c hc
    have := hnd.length_le_of_subset hsub
    omega
  obtain ⟨c, hc, hct⟩ := hsome
  have hf : (cs.find? fun n => decide (n ∉ taken)).isSome := by
    rw [List.find?_isSome]
    exact ⟨c, hc, by simpa using hct⟩
  obtain ⟨r, hr⟩ := Option.isSome_iff_exists.1 hf
  have hp := List.find?_some hr
  rw [hr, Option.getD_some]
  simpa using hp

theorem uniqName_of_notin (taken : List String) (base : String) (h : base ∉ taken) : uniqName taken base = base := by
  simp [uniqName, h]

theorem length_uniqFrom (acc bs : List String) : (uniqFrom acc bs).length = bs.length := by
  induction bs generalizing acc with
  | nil => rfl
  | cons b t ih => simp [uniqFrom, ih]

/-- Each name that `uniqFrom` gives is new: it is none of the names in `acc`. -/
theorem uniqFrom_disjoint (acc bs : List String) : ∀ n ∈ uniqFrom acc bs, n ∉ acc := by
  induction bs generalizing acc with
  | nil => simp [uniqFrom]
  | cons b t ih =>
    intro n hn
    simp only [uniqFrom, List.mem_cons] at hn
    rcases hn with rfl | hn
    · exact uniqName_fresh acc b
    · intro hacc
      exact ih (acc ++ [uniqName acc b]) n hn (List.mem_append_left _ hacc)

/-! ### Emission -/

/-- Mirrors concourse.jobEmitter.finallyFor (provider/concourse/emit_job.go): the finally steps are the hooks of the
job that no other job needs, when there is exactly one. -/
def sinkJob (p : Pipe) : Option Core.Job :=
  match p.jobs.filter (fun j => !(p.jobs.any fun k => k.needs.any fun n => n.job == j.id)) with
  | [j] => some j
  | _ => none

def finSteps (p : Pipe) : List Core.Step := p.fin.flatMap (·.steps)

/-- The names of the hook tasks: the finally step IDs, after the step IDs of the sink job (emit_job.go finallyJob;
the finally steps share `taken` with the plan of the sink job). -/
def hookNames (p : Pipe) : List String :=
  match sinkJob p with
  | some j => uniqFrom (j.steps.map (·.id)) ((finSteps p).map (·.id))
  | none => []

/-- A script that the args of a task read back differently: the parser drops one trailing newline and splits a line
at a newline (parse_step.go command). The emitter then puts the script in the carry (emit_carry.go lossyScript). -/
def lossy (sc : List String) : Bool :=
  (decide (1 < sc.length) && sc.getLast? == some "") || sc.any fun l => decide ('\n' ∈ l.toList)

/-- The lines that the parser reads from the args that hold `sc` joined by newlines. -/
def argLines (sc : List String) : List String :=
  let t := String.intercalate "\n" sc
  (if t.endsWith "\n" then (t.dropEnd 1).toString else t).splitOn "\n"

def cstep (n : String) (s : Core.Step) : CTask :=
  ⟨n, if lossy s.script then argLines s.script else s.script, if lossy s.script then s.script else [],
    s.id, s.name, s.env⟩

def hookTasks (p : Pipe) : List CTask := ((finSteps p).zip (hookNames p)).map fun (s, n) => cstep n s

/-- Mirrors concourse.pipeEmitter.job (provider/concourse/emit_job.go), concourse.jobEmitter.gets
(emit_gets.go) and concourse.jobEmitter.carryJob (emit_carry.go): the job `j` of the pipeline `p`. The `passed` list
holds the need jobs in order, on the shared source `get` that every job writes. -/
def cjob (p : Pipe) (j : Core.Job) : CJob where
  name := j.id
  disp := j.name
  passed := j.needs.map (·.job)
  body := j.steps.map fun s => cstep s.id s
  hook := if sinkJob p = some j then hookTasks p else []
  carry := { j with steps := [] }

/-- Mirrors concourse.Emit (provider/concourse/emit.go) on the core subset: one file. -/
def toConcourse (p : Pipe) : CDoc where
  file := p.id
  carry := { p with jobs := [] }
  jobs := p.jobs.map (cjob p)
  finJob := if hookNames p = [] then "" else ((sinkJob p).map (·.id)).getD ""
  finTasks := hookNames p

/-! ### Parsing -/

/-- Mirrors concourse.jobBuilder.carryStep (provider/concourse/parse_marker.go): the ID, name and env come from the
carry, and the script from the carry when it holds one, else from the file. -/
def readTask (t : CTask) : Core.Step := ⟨t.cid, t.cname, if t.carried = [] then t.script else t.carried, t.cenv⟩

/-- Mirrors concourse.jobBuilder.needs and carryJob: the needs are the `passed` names in order, and the carry gives
the artifacts flag of each when the carry holds as many needs. -/
def readNeeds (j : CJob) : List Core.Need :=
  if j.carry.needs.length = j.passed.length then
    (j.passed.zip j.carry.needs).map fun (n, c) => ⟨n, c.artifacts⟩
  else j.passed.map fun n => ⟨n, false⟩

/-- Mirrors concourse.pipeBuilder.finallyHooks and concourse.jobBuilder.carryJob (parse_marker.go): the hook tasks that
the marker names are taken out of the steps of the job that holds them. -/
def readJob (d : CDoc) (j : CJob) : Core.Job where
  id := j.name
  name := j.disp
  needs := readNeeds j
  env := j.carry.env
  runner := j.carry.runner
  steps :=
    (if j.name == d.finJob then (j.body ++ j.hook).filter (fun t => !d.finTasks.contains t.name) else j.body ++ j.hook).map
      readTask
  retry := j.carry.retry
  timeout := j.carry.timeout
  manual := j.carry.manual
  allowFailure := j.carry.allowFailure
  onFailure := j.carry.onFailure

/-- The pipeline Parse builds before the round-trip metadata is applied (parse_marker.go carryPipeline). -/
def rawConcourse (d : CDoc) : Pipe :=
  { id := d.file
    name := d.carry.name
    push := d.carry.push
    tag := d.carry.tag
    pr := d.carry.pr
    jobs := d.jobs.map (readJob d)
    fin := d.carry.fin }

/-- Mirrors concourse.Parse (provider/concourse/parse.go) on the core subset; the metadata `m` is applied last
(roundtrip.Restore). -/
def fromConcourse (d : CDoc) (m : Meta) : Pipe := restore (rawConcourse d) m

/-! ### The fragment -/

/-- A name that the emitter writes unchanged: it starts with a lower-case letter and holds no upper-case letter
(SanitizeID, provider/concourse/ids.go; `Wf` already limits the characters to letters, digits, `_` and `-`). -/
def CcId (s : String) : Prop :=
  s.toList ≠ [] ∧ (∀ c ∈ s.toList.take 1, c.isLower = true) ∧ ∀ c ∈ s.toList, c.isUpper = false

instance (s : String) : Decidable (CcId s) := by unfold CcId; exact inferInstance

/-- The first characters of the source resource names `source`, `source-<branch>` and `source-tags`
(emit_source.go buildSources). A task named like a resource is renamed (emit_step.go step), and the model leaves that out. -/
def sourcePrefix : List Char := "source".toList

/-- A task name that the emitter writes unchanged. -/
def StepOk (s : Core.Step) : Prop := CcId s.id ∧ sourcePrefix.isPrefixOf s.id.toList = false

instance (s : Core.Step) : Decidable (StepOk s) := by unfold StepOk; exact inferInstance

/-- A script that the emitter writes as given: at least one line (an empty script is written as `true`), and no `((`
in a line (the emitter comments such a line out with the `((` broken to `( (`, so Concourse reads no variable in the
comment, and carries the script instead). -/
def ScriptOk (sc : List String) : Prop :=
  sc ≠ [] ∧ ∀ l ∈ sc, ¬ (['(', '('] <:+: l.toList)

instance (sc : List String) : Decidable (ScriptOk sc) := by unfold ScriptOk; exact inferInstance

/-- What Concourse needs beyond `Wf`: a main job (a pipeline with none gets a placeholder, emit_pipeline.go), main job
names and every task name that the emitter writes unchanged, and scripts that the parser reads back. Finally steps are
written as hook tasks only when one job is the sink, and their scripts are carried whole, so only their names matter. -/
def ConcourseOk (p : Pipe) : Prop :=
  p.jobs ≠ [] ∧ (∀ j ∈ p.jobs, CcId j.id ∧ ∀ s ∈ j.steps, StepOk s ∧ ScriptOk s.script) ∧
    ∀ j ∈ p.fin, ∀ s ∈ j.steps, StepOk s

instance (p : Pipe) : Decidable (ConcourseOk p) := by unfold ConcourseOk; exact inferInstance

/-- The Concourse fragment (Mirrors concourse.Caps (provider/concourse/caps.go)): the shared structure plus
Concourse's own needs. -/
def ConcourseFrag (p : Pipe) : Prop := Wf p ∧ ConcourseOk p

instance (p : Pipe) : Decidable (ConcourseFrag p) := by unfold ConcourseFrag; exact inferInstance

/-! ### Theorem 1: parse inverts emit on the Concourse fragment -/

/-- The needs that the parser reads from `passed` are the IR needs that the emitter wrote, whatever the job: a job with
no step, a manual job and a producer job with no step of its own included. -/
theorem concourse_passed (p : Pipe) (j : Core.Job) :
    (cjob p j).passed = j.needs.map (·.job) ∧ readNeeds (cjob p j) = j.needs := by
  refine ⟨rfl, ?_⟩
  have hl : (cjob p j).carry.needs.length = (cjob p j).passed.length := by simp [cjob]
  unfold readNeeds
  simp only [hl, ↓reduceIte]
  simp only [cjob]
  induction j.needs with
  | nil => rfl
  | cons n t ih => simp [ih]

theorem readTask_cstep (n : String) (s : Core.Step) (h : s.script ≠ []) : readTask (cstep n s) = s := by
  cases s with
  | mk id name script env =>
    by_cases hl : lossy script = true
    · simp only [cstep, hl, ite_true, readTask]
      have : script ≠ [] := h
      simp [this]
    · have hl' : lossy script = false := by simpa using hl
      simp [cstep, hl', readTask]

theorem sinkJob_mem (p : Pipe) (k : Core.Job) (h : sinkJob p = some k) : k ∈ p.jobs := by
  unfold sinkJob at h
  split at h
  · rename_i j hl
    have hjk : k = j := (Option.some.inj h).symm
    subst hjk
    have : k ∈ p.jobs.filter (fun j => !(p.jobs.any fun k => k.needs.any fun n => n.job == j.id)) := by
      rw [hl]; simp
    exact (List.mem_filter.1 this).1
  · cases h

theorem hookNames_ne_nil (p : Pipe) (h : hookNames p ≠ []) : ∃ k, sinkJob p = some k := by
  unfold hookNames at h
  cases hs : sinkJob p with
  | none => simp [hs] at h
  | some k => exact ⟨k, rfl⟩

/-- Two main jobs with one ID are one job. -/
theorem jobs_id_inj (p : Pipe) (hn : (jobIds p.all).Nodup) {a b : Core.Job} (ha : a ∈ p.jobs) (hb : b ∈ p.jobs)
    (h : a.id = b.id) : a = b := by
  have hj : (p.jobs.map Core.Job.id).Nodup := by
    simp only [jobIds, Pipe.all, List.map_append, List.nodup_append] at hn
    exact hn.1
  exact List.inj_on_of_nodup_map hj ha hb h

/-- The steps of a job that the parser reads, after the hook tasks are taken out, are the steps that the emitter wrote. -/
theorem readSteps_cjob (p : Pipe) (hn : (jobIds p.all).Nodup) (j : Core.Job) (hj : j ∈ p.jobs) :
    (if (cjob p j).name == (toConcourse p).finJob then
        ((cjob p j).body ++ (cjob p j).hook).filter (fun t => !(toConcourse p).finTasks.contains t.name)
      else (cjob p j).body ++ (cjob p j).hook) = (cjob p j).body := by
  by_cases h : hookNames p = []
  · have hz : hookTasks p = [] := by simp [hookTasks, h]
    simp [cjob, toConcourse, h, hz]
  · obtain ⟨k, hk⟩ := hookNames_ne_nil p h
    have hfj : (toConcourse p).finJob = k.id := by simp [toConcourse, h, hk]
    have hft : (toConcourse p).finTasks = hookNames p := rfl
    by_cases hjk : sinkJob p = some j
    · have hkj : k = j := by rw [hk] at hjk; exact Option.some.inj hjk
      subst hkj
      have hh : (cjob p k).hook = hookTasks p := by simp [cjob, hk]
      have hb : ∀ t ∈ k.steps.map (fun s => cstep s.id s), t.name ∉ hookNames p := by
        intro t ht
        obtain ⟨s, hs, rfl⟩ := List.mem_map.1 ht
        have := uniqFrom_disjoint (k.steps.map (·.id)) ((finSteps p).map (·.id))
        simp only [hookNames, hk] at this ⊢
        intro hm
        exact this _ hm (List.mem_map_of_mem (f := Core.Step.id) hs)
      have hc : ∀ t ∈ hookTasks p, t.name ∈ hookNames p := by
        intro t ht
        obtain ⟨⟨s, nm⟩, hm, rfl⟩ := List.mem_map.1 ht
        exact (List.of_mem_zip hm).2
      have e1 : List.filter (fun t => !(hookNames p).contains t.name) (k.steps.map (fun s => cstep s.id s)) =
          k.steps.map (fun s => cstep s.id s) := by
        refine List.filter_eq_self.2 fun t ht => ?_
        have := hb t ht
        simpa using this
      have e2 : List.filter (fun t => !(hookNames p).contains t.name) (hookTasks p) = [] := by
        refine List.filter_eq_nil_iff.2 fun t ht => ?_
        have := hc t ht
        simpa using this
      simp only [cjob, hjk, hfj, hft, beq_self_eq_true, ite_true, List.filter_append, e1, e2, List.append_nil]
    · have hkm := sinkJob_mem p k hk
      have hne : j.id ≠ k.id := by
        intro he
        exact hjk (by rw [hk, jobs_id_inj p hn hj hkm he])
      have hh : (cjob p j).hook = [] := by simp [cjob, hjk]
      have hnm : ((cjob p j).name == (toConcourse p).finJob) = false := by
        simp [cjob, hfj, hne]
      simp [hnm, hh]

theorem readJob_cjob (p : Pipe) (hn : (jobIds p.all).Nodup) (j : Core.Job) (hj : j ∈ p.jobs)
    (hsc : ∀ s ∈ j.steps, s.script ≠ []) :
    readJob (toConcourse p) (cjob p j) = j := by
  have hs := readSteps_cjob p hn j hj
  have hneeds := (concourse_passed p j).2
  have hb : (j.steps.map (fun s => cstep s.id s)).map readTask = j.steps := by
    rw [List.map_map]
    exact map_self_mem _ _ fun s hs' => readTask_cstep s.id s (hsc s hs')
  unfold readJob
  rw [hs, hneeds]
  simp only [cjob, hb]

theorem rawConcourse_toConcourse (p : Pipe) (hw : Wf p) (hsc : ∀ j ∈ p.jobs, ∀ s ∈ j.steps, s.script ≠ []) : rawConcourse (toConcourse p) = p := by
  have hn : (jobIds p.all).Nodup := hw.2.1
  have e : (p.jobs.map (cjob p)).map (readJob (toConcourse p)) = p.jobs := by
    rw [List.map_map]
    exact map_self_mem _ _ fun j hj => readJob_cjob p hn j hj (hsc j hj)
  have hr : rawConcourse (toConcourse p) = { p with jobs := (p.jobs.map (cjob p)).map (readJob (toConcourse p)) } := rfl
  rw [hr, e]

theorem scripts_ne_nil (p : Pipe) (h : ConcourseOk p) : ∀ j ∈ p.jobs, ∀ s ∈ j.steps, s.script ≠ [] :=
  fun j hj s hs => ((h.2.1 j hj).2 s hs).2.1

/-- Theorem 1 of the Concourse adapter: parsing the emitted file and applying the metadata returns the pipeline, on
the Concourse fragment. Proved about the model. -/
theorem concourse_from_to (p : Pipe) (h : ConcourseFrag p) : fromConcourse (toConcourse p) (metaOf p) = p := by
  obtain ⟨hw, ho⟩ := h
  unfold fromConcourse
  rw [rawConcourse_toConcourse p hw (scripts_ne_nil p ho)]
  exact restore_metaOf_self p hw.2.1 fun j hj => (hw.2.2.2.2.2.2.2.1 j hj).2

/-! ### The raw parse keeps the fragment, so a chain can run through Concourse

The raw parse of Concourse is the pipeline itself on the fragment (`rawConcourse_toConcourse`), so a hop through
Concourse keeps every fragment (`Chain.Adapter.keeps_of_id`). The converse needs that the raw parse of each other
adapter keeps the Concourse fragment: its job IDs, step IDs and scripts, which every raw parse keeps. -/

/-- A pipeline whose jobs keep what `Drone.Keep` lists keeps the Concourse fragment. -/
theorem concOk_of_shape (p : Pipe) (pid : String) (fm ff : Core.Job → Core.Job) (hm : ∀ j, Drone.Keep j (fm j))
    (hf : ∀ j, Drone.Keep j (ff j)) (q : Pipe)
    (hq : q = { id := pid, name := "", push := p.push, tag := p.tag, pr := p.pr,
                jobs := p.jobs.map fm, fin := p.fin.map ff }) :
    ConcourseOk p → ConcourseOk q := by
  intro ho
  obtain ⟨hne, hjob, hfin⟩ := ho
  subst hq
  refine ⟨by simpa using hne, ?_, ?_⟩
  · intro x hx
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 hx
    obtain ⟨hid, hst⟩ := hjob j hj
    refine ⟨by rw [(hm j).1]; exact hid, ?_⟩
    intro s hs
    rw [(hm j).2.2.2.1] at hs
    obtain ⟨s0, hs0, e1, e2, _⟩ := Woodpecker.mem_stripName hs
    obtain ⟨a, b⟩ := hst s0 hs0
    refine ⟨?_, by rw [e2]; exact b⟩
    unfold StepOk at a ⊢
    rw [e1]; exact a
  · intro x hx s hs
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 hx
    rw [(hf j).2.2.2.1] at hs
    obtain ⟨s0, hs0, e1, _, _⟩ := Woodpecker.mem_stripName hs
    have a := hfin j hj s0 hs0
    unfold StepOk at a ⊢
    rw [e1]; exact a

theorem concOk_clearP (q : Pipe) : ConcourseOk q → ConcourseOk (Tekton.clearP q) := by
  intro h
  obtain ⟨hne, hjob, hfin⟩ := h
  refine ⟨by simpa [Tekton.clearP] using hne, ?_, ?_⟩
  · intro x hx
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 (show x ∈ q.jobs.map Tekton.clr from hx)
    exact hjob j hj
  · intro x hx
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 (show x ∈ q.fin.map Tekton.clr from hx)
    exact hfin j hj

theorem concOk_emptyFin (q : Pipe) : ConcourseOk q → ConcourseOk (Argo.emptyFin q) := by
  intro h
  obtain ⟨hne, hjob, hfin⟩ := h
  refine ⟨hne, hjob, ?_⟩
  intro x hx
  obtain ⟨j, hj, rfl⟩ := List.mem_map.1 (show x ∈ q.fin.map Argo.ef from hx)
  exact hfin j hj

end Adapter

end Concourse
end Pipemaster
