import Mathlib.Data.List.Nodup
import Pipemaster.Dag

namespace Pipemaster
namespace Core

/-! The IR subset the GHA and GitLab adapter models cover (Mirrors ir.CoreOf (ir/core.go)): one pipeline of
script jobs with literal values. Not modelled: expressions (Task 15 proves their translation), matrices
(Task 10), actions, services, containers, caches, artifacts, outputs, environments, schedule and manual
triggers, path and exclude filters, optional needs, retry conditions, step options other than env, and
the Ext passthrough of every node. -/

/-- A literal environment variable. -/
structure Var where
  name : String
  value : String
  deriving DecidableEq, Repr

/-- A script step with literal lines. -/
structure Step where
  id : String
  name : String
  script : List String
  env : List Var
  deriving DecidableEq, Repr

/-- A needs edge; `artifacts` asks for the producer's artifacts. -/
structure Need where
  job : String
  artifacts : Bool
  deriving DecidableEq, Repr

/-- A job. `retry` is the number of runs (0 = none), `timeout` is whole minutes (0 = none), `onFailure`
marks a finally job that runs only after a failure. -/
structure Job where
  id : String
  name : String
  needs : List Need
  env : List Var
  runner : List String
  steps : List Step
  retry : Nat
  timeout : Nat
  manual : Bool
  allowFailure : Bool
  onFailure : Bool
  deriving DecidableEq, Repr

/-- The pipeline. `none` for a trigger means absent; `some []` means a trigger without a filter. -/
structure Pipe where
  id : String
  name : String
  push : Option (List String)
  tag : Option (List String)
  pr : Option (List String)
  jobs : List Job
  fin : List Job
  deriving DecidableEq, Repr

def Pipe.all (p : Pipe) : List Job := p.jobs ++ p.fin

/-! ## IR paths and round-trip metadata -/

/-- The IR path of a node, in parsed form (Go `pipeline/<id>`, `.../job/<id>`, `.../step/<id>`). The Go
strings are injective on valid IDs, which contain no `/`, so the model keys on the parts. -/
inductive Path
  | pipe (pid : String)
  | job (pid jid : String)
  | step (pid jid sid : String)
  deriving DecidableEq, Repr

/-- The pipeline ID the GitLab parser assigns (gitlab.pipelineID). -/
def gitlabPipelineId : String := "gitlab-ci"

/-- Mirrors meta.Meta (meta/meta.go) for the core subset. Each field is a Go map, read as a partial function:
emitted pipeline ID to source ID (`IDs`), source IR path to display name (`Residual`), and source job path to
the whole needs list of a job whose emit report holds a lost `job.needs.artifacts` entry (`Lost`, field Needs).
The Go `Target` field is not modelled: it only decides whether `roundtrip.Hop` accepts a stack. -/
structure Meta where
  ids : String → Option String
  names : Path → Option String
  needs : Path → Option (List Need)

def Meta.empty : Meta := ⟨fun _ => none, fun _ => none, fun _ => none⟩

def nameOf (s : String) : Option String := if s = "" then none else some s

def findJob (p : Pipe) (jid : String) : Option Job := p.all.find? fun j => decide (j.id = jid)

def findStep (j : Job) (sid : String) : Option Step := j.steps.find? fun s => decide (s.id = sid)

/-- Mirrors roundtrip.NewMeta (internal/roundtrip/meta.go) for the core subset.
GitLab renames the pipeline to `gitlab-ci` (roundtrip.IDsFor); the display names go to Residual; a job whose
needs carry an artifacts flag the producer cannot honour goes to Lost (emitJob.needsNode reports it). -/
def metaOf (p : Pipe) : Meta where
  ids := fun q => if q = gitlabPipelineId ∧ p.id ≠ gitlabPipelineId then some p.id else none
  names := fun
    | .pipe pid => if pid = p.id then nameOf p.name else none
    | .job pid jid => if pid = p.id then (findJob p jid).bind fun j => nameOf j.name else none
    | .step pid jid sid =>
      if pid = p.id then (findJob p jid).bind fun j => (findStep j sid).bind fun s => nameOf s.name else none
  needs := fun
    | .job pid jid =>
      if pid = p.id then (findJob p jid).bind fun j => if j.needs.any (·.artifacts) then some j.needs else none
      else none
    | _ => none

/-- Fills only an empty name (meta.fill). -/
def fill (cur : String) (stored : Option String) : String :=
  if cur = "" then stored.getD "" else cur

def restoreStep (m : Meta) (pid jid : String) (s : Step) : Step :=
  { s with name := fill s.name (m.names (.step pid jid s.id)) }

def restoreJob (m : Meta) (pid : String) (j : Job) : Job :=
  { j with
    name := fill j.name (m.names (.job pid j.id))
    needs := (m.needs (.job pid j.id)).getD j.needs
    steps := j.steps.map (restoreStep m pid j.id) }

/-- Mirrors roundtrip.Restore (internal/roundtrip/meta.go): undo the renames, then re-attach names and lost fields. -/
def restore (q : Pipe) (m : Meta) : Pipe :=
  let pid := (m.ids q.id).getD q.id
  { q with
    id := pid
    name := fill q.name (m.names (.pipe pid))
    jobs := q.jobs.map (restoreJob m pid)
    fin := q.fin.map (restoreJob m pid) }

theorem map_self {α : Type} (f : α → α) (h : ∀ x, f x = x) (l : List α) : l.map f = l := by
  induction l with
  | nil => rfl
  | cons a t ih => simp [h, ih]

theorem map_self_mem {α : Type} (f : α → α) (l : List α) (h : ∀ x ∈ l, f x = x) : l.map f = l := by
  induction l with
  | nil => rfl
  | cons a t ih =>
    simp only [List.map_cons, List.cons.injEq]
    exact ⟨h a (by simp), ih fun x hx => h x (by simp [hx])⟩

theorem fill_empty (cur : String) : fill cur none = cur := by
  unfold fill; split <;> simp_all

theorem restore_empty (q : Pipe) : restore q Meta.empty = q := by
  have hs : ∀ (pid jid : String) (s : Step), restoreStep Meta.empty pid jid s = s := by
    intro pid jid s
    cases s; simp [restoreStep, Meta.empty, fill_empty]
  have hj : ∀ (pid : String) (j : Job), restoreJob Meta.empty pid j = j := by
    intro pid j
    have h := map_self (restoreStep Meta.empty pid j.id) (hs pid j.id) j.steps
    cases j
    simp only [restoreJob, Meta.empty, fill_empty, Option.getD_none] at h ⊢
    simp [h]
  cases q with
  | mk id name push tag pr jobs fin =>
    have h1 := map_self (restoreJob Meta.empty id) (hj id) jobs
    have h2 := map_self (restoreJob Meta.empty id) (hj id) fin
    simp only [restore, Meta.empty, fill_empty, Option.getD_none] at h1 h2 ⊢
    simp [h1, h2]

theorem find_id {α : Type} (f : α → String) (l : List α) (x : α) (hx : x ∈ l) (hn : (l.map f).Nodup) :
    l.find? (fun y => decide (f y = f x)) = some x := by
  induction l with
  | nil => simp at hx
  | cons a t ih =>
    rw [List.map_cons, List.nodup_cons] at hn
    rw [List.find?_cons]
    by_cases hax : a = x
    · subst hax; simp
    · have hxt : x ∈ t := by
        rcases List.mem_cons.mp hx with h | h
        · exact absurd h.symm hax
        · exact h
      have hne : f a ≠ f x := fun h => hn.1 (by rw [h]; exact List.mem_map_of_mem hxt)
      simp [hne, ih hxt hn.2]

/-! ## List lemmas for the adapter proofs -/

theorem findSome_flatMap_none {α β γ : Type} (l : List α) (f : α → List β) (q : β → Option γ)
    (h : ∀ y ∈ l, (f y).findSome? q = none) : (l.flatMap f).findSome? q = none := by
  induction l with
  | nil => rfl
  | cons a t ih =>
    rw [List.flatMap_cons, List.findSome?_append, h a (by simp),
      ih fun y hy => h y (by simp [hy])]
    rfl

/-- In a list without repeats, the one element that can answer a query decides a `flatMap` search. -/
theorem findSome_flatMap_unique {α β γ : Type} [DecidableEq α] (l : List α) (f : α → List β)
    (q : β → Option γ) (x : α) (hx : x ∈ l) (hl : l.Nodup)
    (h : ∀ y ∈ l, y ≠ x → (f y).findSome? q = none) :
    (l.flatMap f).findSome? q = (f x).findSome? q := by
  induction l with
  | nil => simp at hx
  | cons a t ih =>
    rw [List.nodup_cons] at hl
    rw [List.flatMap_cons, List.findSome?_append]
    by_cases hax : a = x
    · subst hax
      have : (t.flatMap f).findSome? q = none :=
        findSome_flatMap_none t f q fun y hy => h y (by simp [hy]) (fun e => hl.1 (e ▸ hy))
      rw [this]; cases (f a).findSome? q <;> rfl
    · have hxt : x ∈ t := by
        rcases List.mem_cons.mp hx with e | e
        · exact absurd e.symm hax
        · exact e
      rw [h a (by simp) hax, ih hxt hl.2 fun y hy => h y (by simp [hy])]
      rfl

theorem findSome_map_unique {α β γ : Type} [DecidableEq α] (l : List α) (f : α → β)
    (q : β → Option γ) (x : α) (hx : x ∈ l) (hl : l.Nodup)
    (h : ∀ y ∈ l, y ≠ x → q (f y) = none) :
    (l.map f).findSome? q = q (f x) := by
  have := findSome_flatMap_unique l (fun y => [f y]) q x hx hl (by
    intro y hy hne; simp [h y hy hne])
  rw [List.map_eq_flatMap]; simpa using this

theorem findSome_map_none {α β γ : Type} (l : List α) (f : α → β) (q : β → Option γ)
    (h : ∀ y ∈ l, q (f y) = none) : (l.map f).findSome? q = none := by
  induction l with
  | nil => rfl
  | cons a t ih =>
    rw [List.map_cons, List.findSome?_cons, h a (by simp)]
    exact ih fun y hy => h y (by simp [hy])

/-! ## Fragment predicates -/

/-- Job keys GitLab CI reserves at the top level (gitlab reservedJobKeys, emit_ids.go), compared in lower case. -/
def gitlabReserved : List String :=
  ["image", "services", "stages", "before_script", "after_script", "variables", "include", "default",
   "workflow", "spec", "true", "false", "null", "yes", "no", "on", "off"]

/-- A non-empty id of letters, digits, `_` and `-` that starts with a letter or `_`: valid as a GHA job key
(gha ids.go regexp ^[A-Za-z_][A-Za-z0-9_-]*$), not a key the GitLab emitter renames. -/
def validId (s : String) : Prop :=
  s.toList ≠ [] ∧ (∀ c ∈ s.toList.take 1, c.isAlpha ∨ c = '_') ∧
  (∀ c ∈ s.toList, c.isAlphanum ∨ c = '_' ∨ c = '-') ∧ s.toLower ∉ gitlabReserved

instance (s : String) : Decidable (validId s) := by
  unfold validId; exact inferInstance

def jobIds (js : List Job) : List String := js.map Job.id

/-- The model's DAG input: main jobs with their needs (Dag.layers, theorem 3). -/
def dagJobs (js : List Job) : List Pipemaster.Job :=
  js.map fun j => ⟨j.id, j.needs.map fun n => ⟨n.job, false⟩⟩

def noNeeds (p : Pipe) : Prop := ∀ j ∈ p.all, j.needs = []

instance (p : Pipe) : Decidable (noNeeds p) := by unfold noNeeds; exact inferInstance

def varNames (vs : List Var) : List String := vs.map Var.name

/-- A manual finally job is excluded because the GitLab emitter reports it as lost (see RoundTrip.lean).
Structure both adapters need: unique valid IDs, needs on main jobs forming a DAG (Dag.layers succeeds),
finally jobs waiting only for main jobs, unique env and step IDs, `onFailure` only on finally jobs. -/
def Wf (p : Pipe) : Prop :=
  validId p.id ∧
  (jobIds p.all).Nodup ∧ (∀ j ∈ p.all, validId j.id) ∧
  (Dag.layers (dagJobs p.jobs)).isSome = true ∧
  (∀ j ∈ p.jobs, ∀ n ∈ j.needs, n.job ∈ jobIds p.jobs) ∧
  (∀ j ∈ p.fin, ∀ n ∈ j.needs, n.job ∈ jobIds p.jobs) ∧
  (∀ j ∈ p.all, (j.needs.map (·.job)).Nodup) ∧
  (∀ j ∈ p.all, (varNames j.env).Nodup ∧ (j.steps.map Step.id).Nodup) ∧
  (∀ j ∈ p.all, ∀ s ∈ j.steps, validId s.id ∧ (varNames s.env).Nodup) ∧
  (∀ j ∈ p.jobs, j.onFailure = false) ∧ (∀ j ∈ p.fin, j.manual = false)

instance (p : Pipe) : Decidable (Wf p) := by unfold Wf; exact inferInstance

/-- What GHA needs beyond `Wf`: a job with no runner label or no script gets a placeholder from the
emitter (gha defaultRunner and noopScript, which the parser collapses only after a marker). -/
def GhaOk (p : Pipe) : Prop :=
  ∀ j ∈ p.all, j.runner ≠ [] ∧ j.steps ≠ [] ∧ ∀ s ∈ j.steps, s.script ≠ []

instance (p : Pipe) : Decidable (GhaOk p) := by unfold GhaOk; exact inferInstance

/-- What GitLab needs beyond `Wf`. A pipeline with no trigger gets GitLab's default push and tag
triggers when parsed. GitLab caps retry at 2 (retry attempts at most 3). A finally job is `when: always` or
`on_failure`, which a manual job would replace. A finally job with no needs gets the needs of its stage,
the main jobs, unless no job has any need (then every job shares a stage). -/
def GitlabOk (p : Pipe) : Prop :=
  (p.push.isSome ∨ p.tag.isSome ∨ p.pr.isSome) ∧
  (∀ j ∈ p.all, j.retry ≤ 3) ∧
  (∀ j ∈ p.fin, j.needs = [] → noNeeds p)

instance (p : Pipe) : Decidable (GitlabOk p) := by unfold GitlabOk; exact inferInstance

/-- The GHA fragment (Mirrors gha.Caps (provider/gha/caps.go)): GHA's own needs, plus GitLab's because `cross_roundtrip` runs through GitLab. -/
def InGha (p : Pipe) : Prop := Wf p ∧ GhaOk p ∧ GitlabOk p

/-- The GitLab fragment (Mirrors gitlab.Caps (provider/gitlab/caps.go)): GitLab's own needs, plus GHA's because `cross_roundtrip_rev` runs through GHA. -/
def InGitlab (p : Pipe) : Prop := Wf p ∧ GitlabOk p ∧ GhaOk p

instance (p : Pipe) : Decidable (InGha p) := by unfold InGha; exact inferInstance
instance (p : Pipe) : Decidable (InGitlab p) := by unfold InGitlab; exact inferInstance

/-! ## The metadata of a pipeline restores that pipeline -/

theorem fill_nameOf (s : String) : fill "" (nameOf s) = s := by
  unfold fill nameOf
  by_cases h : s = "" <;> simp [h]

theorem fill_self (s : String) : fill s (nameOf s) = s := by
  unfold fill nameOf
  by_cases h : s = "" <;> simp [h]

theorem restoreStep_self (p : Pipe) (j : Job) (hj : j ∈ p.all) (hn : (p.all.map Job.id).Nodup)
    (hs : (j.steps.map Step.id).Nodup) (s : Step) (hsj : s ∈ j.steps) :
    restoreStep (metaOf p) p.id j.id s = s := by
  have hf : findJob p j.id = some j := find_id Job.id p.all j hj hn
  have hst : findStep j s.id = some s := find_id Step.id j.steps s hsj hs
  unfold restoreStep
  simp only [metaOf, hf, hst, if_true, Option.bind_some]
  rw [fill_self]

theorem restoreJob_self (p : Pipe) (hn : (p.all.map Job.id).Nodup)
    (hs : ∀ j ∈ p.all, (j.steps.map Step.id).Nodup) (j : Job) (hj : j ∈ p.all) :
    restoreJob (metaOf p) p.id j = j := by
  have hf : findJob p j.id = some j := find_id Job.id p.all j hj hn
  have hsteps : j.steps.map (restoreStep (metaOf p) p.id j.id) = j.steps :=
    map_self_mem _ _ fun s hsj => restoreStep_self p j hj hn (hs j hj) s hsj
  have hnames : (metaOf p).names (.job p.id j.id) = nameOf j.name := by simp [metaOf, hf]
  have hneeds : (metaOf p).needs (.job p.id j.id) =
      if j.needs.any (·.artifacts) = true then some j.needs else none := by simp [metaOf, hf]
  unfold restoreJob
  simp only [hnames, hneeds, fill_self, hsteps]
  by_cases hany : j.needs.any (·.artifacts) = true
  · simp [hany]
  · simp [hany]

theorem restore_metaOf_self (p : Pipe) (hn : (p.all.map Job.id).Nodup)
    (hs : ∀ j ∈ p.all, (j.steps.map Step.id).Nodup) : restore p (metaOf p) = p := by
  have hid : (metaOf p).ids p.id = none := by simp [metaOf]
  have hnm : (metaOf p).names (.pipe p.id) = nameOf p.name := by simp [metaOf]
  have e1 : p.jobs.map (restoreJob (metaOf p) p.id) = p.jobs :=
    map_self_mem _ _ fun j hj => restoreJob_self p hn hs j (by simp [Pipe.all, hj])
  have e2 : p.fin.map (restoreJob (metaOf p) p.id) = p.fin :=
    map_self_mem _ _ fun j hj => restoreJob_self p hn hs j (by simp [Pipe.all, hj])
  unfold restore
  simp only [hid, Option.getD_none, hnm, fill_self, e1, e2]

end Core
end Pipemaster
