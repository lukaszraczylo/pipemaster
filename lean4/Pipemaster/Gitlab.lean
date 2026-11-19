import Pipemaster.Core

namespace Pipemaster
namespace Gitlab

open Core

/-! The GitLab adapter on the core subset. `Pipeline` mirrors the keys the emitter writes (provider/gitlab),
not YAML syntax. Not in the abstract AST: the `workflow:rules` text (Task 15 covers the expression
grammar; the parser restores the triggers from the trigger markers when the rules re-derive them) and the
jobs' per-step script text, which `Block` holds as lines. -/

/-- One step's script items: the `# pipemaster:step <id>` marker, the `step.env` marker and subshell
when `env` is set, and the script lines. -/
structure Block where
  id : String
  env : List Var
  lines : List String
  deriving DecidableEq, Repr

/-- One top-level job key. -/
structure Job where
  key : String
  stage : Option String
  tags : List String
  needs : Option (List String)
  vars : List Var
  script : List Block
  retry : Option Nat
  timeout : Option Nat
  allowFailure : Option Bool
  whenKey : Option String
  deriving DecidableEq, Repr

/-- A trigger marker: position in the normalised trigger list, kind (`pr`, `push`, `tag`) and include list. -/
structure TrigMark where
  idx : Nat
  kind : String
  incl : List String
  deriving DecidableEq, Repr

structure Pipeline where
  stages : List String
  /-- `workflow:rules` as (kind, include) pairs, one per trigger; `none` when the key is absent. -/
  rules : Option (List TrigMark)
  jobs : List Job
  marks : List TrigMark
  deriving DecidableEq, Repr

/-! ## Constants of the emitter and parser -/

def stagePrefix : String := "stage-"
def stagePost : String := ".post"
def stagePre : String := ".pre"
/-- GitLab's default stages (gitlab defaultStages) and the stage of a job with no `stage:` key. -/
def defaultStages : List String := ["build", "test", "deploy"]
def defaultStage : String := "test"
def whenManual : String := "manual"
def whenAlways : String := "always"
def whenOnFailure : String := "on_failure"
def kindPr : String := "pr"
def kindPush : String := "push"
def kindTag : String := "tag"

/-! ## Emission -/

def stageName (k : Nat) : String := stagePrefix ++ toString k

/-- Whether every job of the project is free of needs: the stage layout then needs no stage keys
(gitlab restoreStages succeeds, since no finally job needs a main job). -/
def allFree (p : Pipe) : Bool := p.all.all (·.needs.isEmpty)

/-- Mirrors gitlab.layerStages (provider/gitlab/emit_stages.go): the main jobs go to `stage-<layer>` when there is more than one layer. -/
def layerList (p : Pipe) : List (List String) :=
  (Dag.layers (dagJobs p.jobs)).getD []

def stageOfMain (p : Pipe) (j : Core.Job) : Option String :=
  if allFree p ∨ (layerList p).length ≤ 1 then none
  else some (stageName (Dag.layerOf (layerList p) j.id))

def stageOfFin (p : Pipe) : Option String :=
  if allFree p then none else some stagePost

def stagesOf (p : Pipe) : List String :=
  if allFree p ∨ (layerList p).length ≤ 1 then [] else (List.range (layerList p).length).map stageName

def blockOf (s : Core.Step) : Block := ⟨s.id, s.env, s.script⟩

def emitJob (stage : Option String) (needs : Option (List String)) (whenKey : Option String)
    (allowFailure : Option Bool) (j : Core.Job) : Job where
  key := j.id
  stage := stage
  tags := j.runner
  needs := needs
  vars := j.env
  script := j.steps.map blockOf
  retry := if j.retry = 0 then none else some (j.retry - 1)
  timeout := if j.timeout = 0 then none else some j.timeout
  allowFailure := allowFailure
  whenKey := whenKey

/-- Manual jobs allow failure by default, so only the other value is written. -/
def mainJob (p : Pipe) (j : Core.Job) : Job :=
  emitJob (stageOfMain p j) (some (j.needs.map (·.job)))
    (if j.manual then some whenManual else none)
    (if j.manual then (if j.allowFailure then none else some false)
     else (if j.allowFailure then some true else none)) j

/-- A finally job with no needs waits for its stage, so the emitter omits `needs`. -/
def finJob (p : Pipe) (j : Core.Job) : Job :=
  emitJob (stageOfFin p) (if j.needs.isEmpty then none else some (j.needs.map (·.job)))
    (some (if j.onFailure then whenOnFailure else whenAlways))
    (if j.allowFailure then some true else none) j

def trigMarks (p : Pipe) : List TrigMark :=
  let ts : List (String × Option (List String)) := [(kindPr, p.pr), (kindPush, p.push), (kindTag, p.tag)]
  let set := ts.filterMap fun (k, f) => f.map fun l => (k, l)
  (set.zipIdx).map fun ((k, l), i) => ⟨i, k, l⟩

/-- The default triggers (gitlab defaultTriggers): an unfiltered push and tag, which no `workflow` key writes. -/
def isDefaultTriggers (p : Pipe) : Bool := p.push = some [] ∧ p.tag = some [] ∧ p.pr = none

/-- Only a pull request branch filter has no rule form that parses back, so it needs the markers. -/
def needsMarks (p : Pipe) : Bool := match p.pr with
  | some (_ :: _) => true
  | _ => false

/-- Mirrors gitlab.Emit (provider/gitlab/emit.go) on the core subset. -/
def toGitlab (p : Pipe) : Pipeline where
  stages := stagesOf p
  rules := if isDefaultTriggers p ∨ trigMarks p = [] then none else some (trigMarks p)
  jobs := p.jobs.map (mainJob p) ++ p.fin.map (finJob p)
  marks := if needsMarks p then trigMarks p else []

/-! ## Parsing -/

/-- Position of a stage in `.pre`, the declared stages, `.post` (gitlab wire, stageIndex). -/
def stageIdx (g : Pipeline) (s : Option String) : Nat :=
  let order := [stagePre] ++ (if g.stages.isEmpty then defaultStages else g.stages) ++ [stagePost]
  order.idxOf (s.getD defaultStage)

def isFin (j : Job) : Bool := j.whenKey = some whenAlways ∨ j.whenKey = some whenOnFailure

/-- jobEdges: a job with no `needs` key waits for every non-finally job of an earlier stage. -/
def derivedNeeds (g : Pipeline) (j : Job) : List String :=
  (g.jobs.filter fun o => !isFin o && stageIdx g o.stage < stageIdx g j.stage).map (·.key)

def parseJob (g : Pipeline) (j : Job) : Core.Job where
  id := j.key
  name := ""
  needs := (j.needs.getD (derivedNeeds g j)).map fun n => ⟨n, false⟩
  env := j.vars
  runner := j.tags
  steps := j.script.map fun b => ⟨b.id, "", b.lines, b.env⟩
  retry := match j.retry with
    | some n => n + 1
    | none => 0
  timeout := j.timeout.getD 0
  manual := decide (j.whenKey = some whenManual)
  allowFailure := match j.allowFailure with
    | some b => b
    | none => decide (j.whenKey = some whenManual)
  onFailure := decide (j.whenKey = some whenOnFailure)

/-- The markers win over the rules (restoreTriggers); a file with no `workflow` key has the default triggers. -/
def parseTriggers (g : Pipeline) (k : String) : Option (List String) :=
  match g.marks, g.rules with
  | [], none => if k = kindPr then none else some []
  | [], some rs => rs.findSome? fun m => if m.kind = k then some m.incl else none
  | ms, _ => ms.findSome? fun m => if m.kind = k then some m.incl else none

/-- The pipeline Parse builds before the round-trip metadata is applied. -/
def rawGitlab (g : Pipeline) : Pipe :=
  { id := gitlabPipelineId
    name := ""
    push := parseTriggers g kindPush
    tag := parseTriggers g kindTag
    pr := parseTriggers g kindPr
    jobs := (g.jobs.filter fun j => !isFin j).map (parseJob g)
    fin := (g.jobs.filter isFin).map (parseJob g) }

/-- Mirrors gitlab.Parse (provider/gitlab/parse.go) on the core subset; the round-trip metadata `m`
is applied last (roundtrip.Restore). -/
def fromGitlab (g : Pipeline) (m : Meta) : Pipe := restore (rawGitlab g) m

/-- Go parses first and restores after (internal/cli/convert.go): the metadata acts on the parsed pipeline only. -/
theorem _root_.Pipemaster.Core.fromGitlab_restore (g : Pipeline) (m : Meta) :
    fromGitlab g m = restore (fromGitlab g Meta.empty) m := by
  unfold fromGitlab
  rw [restore_empty]

/-! ## Theorem 2: parse inverts emit on the GitLab fragment -/

theorem parseTriggers_ok (p : Pipe) (h : p.push.isSome ∨ p.tag.isSome ∨ p.pr.isSome) :
    parseTriggers (toGitlab p) kindPush = p.push ∧ parseTriggers (toGitlab p) kindTag = p.tag ∧
    parseTriggers (toGitlab p) kindPr = p.pr := by
  rcases hpr : p.pr with _ | _ | ⟨a, as⟩ <;> rcases hpu : p.push with _ | _ | ⟨b, bs⟩ <;>
    rcases hta : p.tag with _ | _ | ⟨c, cs⟩ <;>
    simp_all [parseTriggers, toGitlab, trigMarks, isDefaultTriggers, needsMarks, kindPr, kindPush, kindTag,
      List.zipIdx]

/-- What GitLab keeps of a job: no display names, no artifacts flags on needs. -/
def strip (j : Core.Job) : Core.Job :=
  { j with name := ""
           needs := j.needs.map fun n => { n with artifacts := false }
           steps := j.steps.map fun s => { s with name := "" } }

theorem retry_rt (r : Nat) :
    (match (if r = 0 then none else some (r - 1)) with | some n => n + 1 | none => 0) = r := by
  by_cases h : r = 0
  · simp [h]
  · simp [h]
    exact Nat.sub_add_cancel (Nat.pos_of_ne_zero h)

theorem timeout_rt' (t : Nat) :
    (if t = 0 then (none : Option Nat) else some t).getD 0 = t := by
  split <;> simp_all

theorem parse_main (p : Pipe) (j : Core.Job) (hon : j.onFailure = false) :
    parseJob (toGitlab p) (mainJob p j) = strip j := by
  unfold parseJob mainJob emitJob strip
  rcases j with ⟨id, name, needs, env, runner, steps, retry, timeout, manual, allow, onf⟩
  simp only [Core.Job.mk.injEq, true_and]
  simp only [] at hon
  subst hon
  refine ⟨?_, ?_, retry_rt _, timeout_rt' _, ?_, ?_, ?_⟩
  · simp [Function.comp_def]
  · simp [blockOf, Function.comp_def]
  all_goals cases manual <;> cases allow <;> simp [whenManual, whenOnFailure]

theorem allFree_of_noNeeds (p : Pipe) (h : noNeeds p) : allFree p = true := by
  unfold allFree
  rw [List.all_eq_true]
  intro j hj
  simp [h j hj]

theorem stages_none (p : Pipe) (h : allFree p = true) :
    ∀ o ∈ (toGitlab p).jobs, o.stage = none := by
  intro o ho
  simp only [toGitlab, List.mem_append, List.mem_map] at ho
  rcases ho with ⟨j, _, rfl⟩ | ⟨j, _, rfl⟩ <;> simp [mainJob, finJob, emitJob, stageOfMain, stageOfFin, h]

theorem derived_nil (p : Pipe) (h : allFree p = true) (j : Job) (hj : j.stage = none) :
    derivedNeeds (toGitlab p) j = [] := by
  unfold derivedNeeds
  rw [List.map_eq_nil_iff, List.filter_eq_nil_iff]
  intro o ho
  simp [stages_none p h o ho, hj]

theorem parse_fin (p : Pipe) (j : Core.Job) (hman : j.manual = false) (hfree : j.needs = [] → noNeeds p) :
    parseJob (toGitlab p) (finJob p j) = strip j := by
  unfold parseJob finJob emitJob strip
  rcases j with ⟨id, name, needs, env, runner, steps, retry, timeout, manual, allow, onf⟩
  simp only [] at hman hfree
  subst hman
  have hnn : needs = [] → derivedNeeds (toGitlab p)
      { key := id, stage := stageOfFin p, tags := runner, needs := none, vars := env,
        script := steps.map blockOf, retry := if retry = 0 then none else some (retry - 1),
        timeout := if timeout = 0 then none else some timeout,
        allowFailure := if allow = true then some true else none,
        whenKey := some (if onf = true then whenOnFailure else whenAlways) } = [] := by
    intro hn
    have hf := allFree_of_noNeeds p (hfree hn)
    exact derived_nil p hf _ (by simp [stageOfFin, hf])
  simp only [Core.Job.mk.injEq, true_and]
  refine ⟨?_, ?_, retry_rt _, timeout_rt' _, ?_, ?_, ?_⟩
  · cases needs with
    | nil => simp [hnn rfl]
    | cons n ns => simp [Function.comp_def]
  · simp [blockOf, Function.comp_def]
  all_goals cases onf <;> cases allow <;> simp [whenManual, whenOnFailure, whenAlways]

theorem restoreStep_strip (p : Pipe) (j : Core.Job) (hj : j ∈ p.all) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : (j.steps.map Core.Step.id).Nodup) (s : Core.Step) (hsj : s ∈ j.steps) :
    restoreStep (metaOf p) p.id j.id { s with name := "" } = s := by
  have hf : findJob p j.id = some j := find_id Core.Job.id p.all j hj hn
  have hst : findStep j s.id = some s := find_id Core.Step.id j.steps s hsj hs
  unfold restoreStep
  simp only [metaOf, hf, hst, if_true, Option.bind_some]
  rw [fill_nameOf]

theorem restoreJob_strip (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup)
    (hs : ∀ j ∈ p.all, (j.steps.map Core.Step.id).Nodup) (j : Core.Job) (hj : j ∈ p.all) :
    restoreJob (metaOf p) p.id (strip j) = j := by
  have hf : findJob p j.id = some j := find_id Core.Job.id p.all j hj hn
  have hsteps : (j.steps.map fun s => { s with name := "" }).map (restoreStep (metaOf p) p.id j.id) = j.steps := by
    rw [List.map_map]
    apply map_self_mem
    intro s hsj
    exact restoreStep_strip p j hj hn (hs j hj) s hsj
  have hnames : (metaOf p).names (.job p.id j.id) = nameOf j.name := by
    simp [metaOf, hf]
  have hneeds : (metaOf p).needs (.job p.id j.id) =
      if j.needs.any (·.artifacts) = true then some j.needs else none := by
    simp [metaOf, hf]
  have hneeds' : (if j.needs.any (·.artifacts) = true then some j.needs else none).getD
      (j.needs.map fun n => { n with artifacts := false }) = j.needs := by
    by_cases hany : j.needs.any (·.artifacts) = true
    · simp [hany]
    · have hall : ∀ n ∈ j.needs, n.artifacts = false := by
        intro n hn'
        simpa using fun h => hany (List.any_eq_true.mpr ⟨n, hn', h⟩)
      simp only [hany, if_false, Option.getD_none]
      exact map_self_mem _ j.needs (fun n hn' => by
        have := hall n hn'
        cases n; simp_all)
  have hsteps' : List.map (restoreStep (metaOf p) p.id j.id ∘ fun s => ({ s with name := "" } : Core.Step)) j.steps
      = j.steps := by
    rw [← List.map_map]; exact hsteps
  unfold restoreJob strip
  simp only [hnames, hneeds, fill_nameOf, List.map_map]
  rw [hneeds', hsteps']

theorem isFin_main (p : Pipe) (j : Core.Job) : isFin (mainJob p j) = false := by
  unfold isFin mainJob emitJob
  cases j.manual <;> simp [whenManual, whenAlways, whenOnFailure]

theorem isFin_fin (p : Pipe) (j : Core.Job) : isFin (finJob p j) = true := by
  unfold isFin finJob emitJob
  cases j.onFailure <;> simp [whenManual, whenAlways, whenOnFailure]

theorem rawGitlab_toGitlab (p : Pipe) (hw : Wf p) (hg : GitlabOk p) :
    rawGitlab (toGitlab p) =
      { id := gitlabPipelineId, name := "", push := p.push, tag := p.tag, pr := p.pr,
        jobs := p.jobs.map strip, fin := p.fin.map strip } := by
  obtain ⟨_, _, _, _, _, _, _, _, _, hon, hman⟩ := hw
  obtain ⟨hany, _, hfree⟩ := hg
  obtain ⟨h1, h2, h3⟩ := parseTriggers_ok p hany
  have hM1 : (p.jobs.map (mainJob p)).filter (fun j => !isFin j) = p.jobs.map (mainJob p) :=
    List.filter_eq_self.mpr (by
      intro x hx
      obtain ⟨j, _, rfl⟩ := List.mem_map.mp hx
      simp [isFin_main])
  have hM2 : (p.fin.map (finJob p)).filter (fun j => !isFin j) = [] :=
    List.filter_eq_nil_iff.mpr (by
      intro x hx
      obtain ⟨j, _, rfl⟩ := List.mem_map.mp hx
      simp [isFin_fin])
  have hF1 : (p.jobs.map (mainJob p)).filter isFin = [] :=
    List.filter_eq_nil_iff.mpr (by
      intro x hx
      obtain ⟨j, _, rfl⟩ := List.mem_map.mp hx
      simp [isFin_main])
  have hF2 : (p.fin.map (finJob p)).filter isFin = p.fin.map (finJob p) :=
    List.filter_eq_self.mpr (by
      intro x hx
      obtain ⟨j, _, rfl⟩ := List.mem_map.mp hx
      simp [isFin_fin])
  have hmain : (toGitlab p).jobs.filter (fun j => !isFin j) = p.jobs.map (mainJob p) := by
    show (p.jobs.map (mainJob p) ++ p.fin.map (finJob p)).filter _ = _
    rw [List.filter_append, hM1, hM2]; simp
  have hfin : (toGitlab p).jobs.filter isFin = p.fin.map (finJob p) := by
    show (p.jobs.map (mainJob p) ++ p.fin.map (finJob p)).filter _ = _
    rw [List.filter_append, hF1, hF2]; simp
  unfold rawGitlab
  rw [hmain, hfin, h1, h2, h3]
  simp only [List.map_map]
  congr 1
  · exact List.map_congr_left fun j hj => parse_main p j (hon j hj)
  · exact List.map_congr_left fun j hj => parse_fin p j (hman j hj) (hfree j hj)

theorem gitlab_from_to (p : Pipe) (h : InGitlab p) : fromGitlab (toGitlab p) (metaOf p) = p := by
  obtain ⟨hw, hg, _⟩ := h
  have hraw := rawGitlab_toGitlab p hw hg
  obtain ⟨_, hnd, _, _, _, _, _, hsteps, _, _, _⟩ := hw
  have hn : (p.all.map Core.Job.id).Nodup := hnd
  unfold fromGitlab
  rw [hraw]
  have hid : (metaOf p).ids gitlabPipelineId = if p.id ≠ gitlabPipelineId then some p.id else none := by
    simp [metaOf]
  have hjs : ∀ j ∈ p.jobs, restoreJob (metaOf p) p.id (strip j) = j := fun j hj =>
    restoreJob_strip p hn (fun j hj => (hsteps j hj).2) j (by simp [Pipe.all, hj])
  have hfs : ∀ j ∈ p.fin, restoreJob (metaOf p) p.id (strip j) = j := fun j hj =>
    restoreJob_strip p hn (fun j hj => (hsteps j hj).2) j (by simp [Pipe.all, hj])
  have hpid : ((metaOf p).ids gitlabPipelineId).getD gitlabPipelineId = p.id := by
    rw [hid]; by_cases h : p.id = gitlabPipelineId <;> simp [h]
  have hnm : fill "" ((metaOf p).names (.pipe p.id)) = p.name := by
    simp only [metaOf, if_true]; exact fill_nameOf _
  unfold restore
  simp only [hpid, hnm, List.map_map]
  have e1 : List.map (restoreJob (metaOf p) p.id ∘ strip) p.jobs = p.jobs :=
    map_self_mem (restoreJob (metaOf p) p.id ∘ strip) p.jobs fun j hj => hjs j hj
  have e2 : List.map (restoreJob (metaOf p) p.id ∘ strip) p.fin = p.fin :=
    map_self_mem (restoreJob (metaOf p) p.id ∘ strip) p.fin fun j hj => hfs j hj
  rw [e1, e2]

end Gitlab
end Pipemaster
