import Pipemaster.Core

namespace Pipemaster
namespace Gha

open Core

/-! The GHA adapter on the core subset. `Workflow` mirrors the keys the emitter writes (provider/gha), not
YAML syntax; `marks` lists the distinct `# pipemaster:emulated` markers with their decoded argument, because
the parser reads them (collapse.go). -/

/-- One workflow step; `id` and `name` are empty when the key is absent. -/
structure Step where
  id : String
  name : String
  uses : Option String
  run : List String
  env : List Var
  deriving DecidableEq, Repr

/-- One job under `jobs:`; `cond` is the `if:` text. -/
structure Job where
  key : String
  name : String
  needs : List String
  cond : Option String
  runsOn : List String
  timeout : Option Nat
  continueOnError : Bool
  env : List Var
  steps : List Step
  deriving DecidableEq, Repr

/-- The `on:` block. `push` holds the `branches` and `tags` lists of the push event, `none` when the event
is absent; a push event with neither list is `some (none, none)`. -/
structure On where
  pr : Option (List String)
  push : Option (Option (List String) × Option (List String))
  dispatch : Bool
  deriving DecidableEq, Repr

/-- Emulation markers (emitkit.Marker and its argument line); paths are reduced to what the parser reads. -/
inductive Mark
  | trigPush (idx : Nat)
  | trigTag (idx : Nat)
  | trigManual
  | jobManual (job : String)
  | jobRetry (job : String) (attempts : Nat)
  | jobNeedsArt (job : String) (needs : List String) (pre : Nat)
  | finally (job : String) (needs : List Core.Need)
  | finallyRoot
  deriving DecidableEq, Repr

/-- A workflow file; `id` is the file stem. -/
structure Workflow where
  id : String
  name : String
  on : On
  jobs : List Job
  marks : List Mark
  deriving DecidableEq, Repr

/-! ## Constants of the emitter -/

/-- The match-all glob the emitter writes for an unfiltered branch or tag list next to a filtered one. -/
def matchAll : String := "**"
/-- actions.Reverse("download") as actionString writes it. -/
def downloadUses : String := "actions/download-artifact@v8"
def condDispatch : String := "github.event_name == 'workflow_dispatch'"
def condAlways : String := "always()"
def condAlwaysFailure : String := "always() && failure()"

/-- The first lines of a retry loop around a step script (gha emit_step.go). -/
def retryHead (n : Nat) : List String :=
  ["for pipemaster_try in $(seq 1 " ++ toString n ++ "); do",
   "  echo \"pipemaster: attempt ${pipemaster_try} of " ++ toString n ++ "\" >&2",
   "  set +e", "  (", "  set -e"]

/-- The last lines of a retry loop. -/
def retryTail : List String :=
  ["  )", "  pipemaster_rc=$?", "  set -e", "  [ \"$pipemaster_rc\" -eq 0 ] && exit 0", "done",
   "exit \"$pipemaster_rc\""]

def wrapRetry (n : Nat) (l : List String) : List String :=
  if n = 0 then l else retryHead n ++ l ++ retryTail

/-- Mirrors gha.unwrapRetry (provider/gha/collapse.go); the model compares the loop lines for equality
where Go checks the prefix of the first line and the trimmed text of the others. -/
def unwrapRetry (n : Nat) (l : List String) : Option (List String) :=
  if 11 ≤ l.length ∧ l.take 5 = retryHead n ∧ l.drop (l.length - 6) = retryTail then
    some ((l.drop 5).take (l.length - 11))
  else none

/-! ## Emission -/

def downloadStep : Step := ⟨"", "", some downloadUses, [], []⟩

def scriptStep (retry : Nat) (s : Core.Step) : Step :=
  ⟨s.id, s.name, none, wrapRetry retry s.script, s.env⟩

def artifactNeeds (j : Core.Job) : List String :=
  (j.needs.filter (·.artifacts)).map (·.job)

def emitJob (needs : List String) (cond : Option String) (j : Core.Job) : Job where
  key := j.id
  name := j.name
  needs := needs
  cond := cond
  runsOn := j.runner
  timeout := if j.timeout = 0 then none else some j.timeout
  continueOnError := j.allowFailure
  env := j.env
  steps := (artifactNeeds j).map (fun _ => downloadStep) ++ j.steps.map (scriptStep j.retry)

def mainJob (j : Core.Job) : Job :=
  emitJob (j.needs.map (·.job)) (if j.manual then some condDispatch else none) j

/-- A finally job waits for every main job and runs under `always()`. -/
def finJob (mains : List String) (j : Core.Job) : Job :=
  emitJob mains (some (if j.onFailure then condAlwaysFailure else condAlways)) j

/-- An unfiltered push and tag pair is a bare push event, which GHA fires for branches and tags. Any other
unfiltered side is written as the match-all glob, so the event fires for the ref kinds the pipeline names. -/
def pushBranches (p : Pipe) : Option (List String) :=
  match p.push with
  | none => none
  | some [] => if p.tag = some [] then none else some [matchAll]
  | some bs => some bs

def pushTags (p : Pipe) : Option (List String) :=
  match p.tag with
  | none => none
  | some [] => if p.push = some [] then none else some [matchAll]
  | some ts => some ts

def toOn (p : Pipe) : On where
  pr := p.pr
  push := if p.push.isSome ∨ p.tag.isSome then some (pushBranches p, pushTags p) else none
  dispatch := p.all.any (·.manual)

def jobMarks (j : Core.Job) : List Mark :=
  (if j.manual then [Mark.jobManual j.id] else []) ++
  (if j.retry = 0 then [] else [Mark.jobRetry j.id j.retry]) ++
  (if (artifactNeeds j).isEmpty then [] else [Mark.jobNeedsArt j.id (artifactNeeds j) (artifactNeeds j).length])

def marksOf (p : Pipe) : List Mark :=
  let off := if p.pr.isSome then 1 else 0
  (if p.push = some [] ∧ p.tag ≠ some [] then [Mark.trigPush off] else []) ++
  (if p.tag = some [] ∧ p.push ≠ some [] then [Mark.trigTag (off + if p.push.isSome then 1 else 0)] else []) ++
  (if p.all.any (·.manual) then [Mark.trigManual] else []) ++
  p.all.flatMap jobMarks ++
  p.fin.map (fun j => Mark.finally j.id j.needs) ++
  (if p.fin.isEmpty then [] else [Mark.finallyRoot])

/-- Mirrors gha.Emit (provider/gha/emit.go) on the core subset. -/
def toGha (p : Pipe) : Workflow where
  id := p.id
  name := p.name
  on := toOn p
  jobs := p.jobs.map mainJob ++ p.fin.map (finJob (p.jobs.map (·.id)))
  marks := marksOf p

/-! ## Parsing -/

def parseJob (j : Job) : Core.Job where
  id := j.key
  name := j.name
  needs := j.needs.map fun n => ⟨n, false⟩
  env := j.env
  runner := j.runsOn
  steps := j.steps.map fun s => ⟨s.id, s.name, s.run, s.env⟩
  retry := 0
  timeout := j.timeout.getD 0
  manual := false
  allowFailure := j.continueOnError
  onFailure := false

def artQ (k : String) : Mark → Option (List String × Nat)
  | .jobNeedsArt j ns pre => if j = k then some (ns, pre) else none
  | _ => none

def retryQ (k : String) : Mark → Option Nat
  | .jobRetry j n => if j = k then some n else none
  | _ => none

def manualQ (k : String) : Mark → Bool
  | .jobManual j => decide (j = k)
  | _ => false

def finQ (k : String) : Mark → Option (List Core.Need)
  | .finally j ns => if j = k then some ns else none
  | _ => none

def trigQ (tag : Bool) : Mark → Bool
  | .trigPush _ => !tag
  | .trigTag _ => tag
  | _ => false

def Workflow.artMark (g : Workflow) (k : String) : Option (List String × Nat) := g.marks.findSome? (artQ k)

def Workflow.retryMark (g : Workflow) (k : String) : Option Nat := g.marks.findSome? (retryQ k)

def Workflow.hasManual (g : Workflow) (k : String) : Bool := g.marks.any (manualQ k)

def Workflow.finMark (g : Workflow) (k : String) : Option (List Core.Need) := g.marks.findSome? (finQ k)

def Workflow.hasTrig (g : Workflow) (tag : Bool) : Bool := g.marks.any (trigQ tag)

/-- collapseCarried: drop the steps that stand in for artifact downloads and restore the flags. -/
def collapseCarried (g : Workflow) (gj : Job) (j : Core.Job) : Core.Job :=
  match g.artMark gj.key with
  | some (ns, pre) =>
    if pre ≤ j.steps.length then
      { j with steps := j.steps.drop pre
               needs := j.needs.map fun n => { n with artifacts := decide (n.job ∈ ns) } }
    else j
  | none => j

/-- collapseRetry: unwrap the loop of every step and restore the retry policy. -/
def collapseRetry (g : Workflow) (gj : Job) (j : Core.Job) : Core.Job :=
  match g.retryMark gj.key with
  | some n =>
    { j with retry := n
             steps := j.steps.map fun s => { s with script := (unwrapRetry n s.script).getD s.script } }
  | none => j

/-- collapseManual: the dispatch guard becomes the manual flag. -/
def collapseManual (g : Workflow) (gj : Job) (j : Core.Job) : Core.Job :=
  if g.hasManual gj.key then
    if gj.cond = none ∨ gj.cond = some condDispatch then { j with manual := true } else j
  else j

def sameSet (a b : List String) : Bool :=
  a.all (b.contains ·) && b.all (a.contains ·)

/-- collapseFinally: the finally mark and an `always()` guard move the job to the finally list. -/
def finOf (g : Workflow) (gj : Job) : Option (Bool × List Core.Need) :=
  match g.finMark gj.key with
  | none => none
  | some ns =>
    if gj.cond = some condAlways then some (false, ns)
    else if gj.cond = some condAlwaysFailure then some (true, ns)
    else none

/-- The IDs of the jobs that carry no finally mark (restoreFinallyNeeds, `want`). -/
def Workflow.mainKeys (g : Workflow) : List String :=
  (g.jobs.filter fun j => (g.finMark j.key).isNone).map (·.key)

def mainOf (g : Workflow) (gj : Job) : Core.Job :=
  collapseManual g gj (collapseRetry g gj (collapseCarried g gj (parseJob gj)))

def finishFin (g : Workflow) (gj : Job) (ob : Bool) (ns : List Core.Need) : Core.Job :=
  let j := collapseRetry g gj (collapseCarried g gj (parseJob gj))
  let want := g.mainKeys ++ ns.map (·.job)
  { j with onFailure := ob
           needs := if sameSet want (j.needs.map (·.job)) then ns else j.needs }

def parseOnPush (g : Workflow) : Option (List String) × Option (List String) :=
  match g.on.push with
  | none => (none, none)
  | some (none, none) => (some [], some [])
  | some (b, t) =>
    (b.map fun bs => if bs = [matchAll] ∧ g.hasTrig false then [] else bs,
     t.map fun ts => if ts = [matchAll] ∧ g.hasTrig true then [] else ts)

def mainPick (g : Workflow) (gj : Job) : Option Core.Job :=
  if (finOf g gj).isNone then some (mainOf g gj) else none

def finPick (g : Workflow) (gj : Job) : Option Core.Job :=
  (finOf g gj).map fun (ob, ns) => finishFin g gj ob ns

/-- The pipeline Parse builds before the round-trip metadata is applied. -/
def rawGha (g : Workflow) : Pipe :=
  { id := g.id
    name := g.name
    push := (parseOnPush g).1
    tag := (parseOnPush g).2
    pr := g.on.pr
    jobs := g.jobs.filterMap (mainPick g)
    fin := g.jobs.filterMap (finPick g) }

/-- Mirrors gha.Parse (provider/gha/parse.go) with collapseMarkers, on the core subset; the
round-trip metadata `m` is applied last (roundtrip.Restore). The core subset has no conditions, so the per-directory
rule for restored source conditions does not apply. -/
def fromGha (g : Workflow) (m : Meta := Meta.empty) : Pipe := restore (rawGha g) m

/-! ## Theorem 1: parse inverts emit on the GHA fragment -/

theorem unwrap_wrap (n : Nat) (hn : n ≠ 0) (l : List String) :
    unwrapRetry n (wrapRetry n l) = some l := by
  have h1 : (retryHead n).length = 5 := by simp [retryHead]
  have h2 : retryTail.length = 6 := by simp [retryTail]
  have hl : (retryHead n ++ l ++ retryTail).length = 11 + l.length := by
    simp [h1, h2]; omega
  have e1 : (retryHead n ++ l ++ retryTail).take 5 = retryHead n := by
    rw [List.append_assoc, ← h1, List.take_left]
  have e2 : (retryHead n ++ l ++ retryTail).drop ((retryHead n ++ l ++ retryTail).length - 6) = retryTail := by
    rw [hl, show 11 + l.length - 6 = (retryHead n ++ l).length by simp [h1]; omega, List.drop_left]
  have e3 : ((retryHead n ++ l ++ retryTail).drop 5).take ((retryHead n ++ l ++ retryTail).length - 11) = l := by
    rw [hl, List.append_assoc, ← h1, List.drop_left, show 11 + l.length - 11 = l.length by omega,
      List.take_left']
    rfl
  rw [wrapRetry, if_neg hn, unwrapRetry, if_pos ⟨by omega, e1, e2⟩, e3]


theorem idne {p : Pipe} (hn : (p.all.map Core.Job.id).Nodup) {y j : Core.Job} (hy : y ∈ p.all)
    (hj : j ∈ p.all) (h : y ≠ j) : y.id ≠ j.id :=
  fun e => h (List.inj_on_of_nodup_map hn hy hj e)

theorem jobMarks_no_trig (j : Core.Job) (tag : Bool) : (jobMarks j).any (trigQ tag) = false := by
  simp [jobMarks, trigQ]

theorem any_ite {α : Type} (c : Prop) [Decidable c] (m : α) (q : α → Bool) :
    (if c then [m] else []).any q = (decide c && q m) := by
  split <;> simp_all

theorem hasTrig_toGha (p : Pipe) (tag : Bool) :
    (toGha p).hasTrig tag = (if tag then decide (p.tag = some [] ∧ p.push ≠ some []) else decide (p.push = some [] ∧ p.tag ≠ some [])) := by
  have hf : (p.all.flatMap jobMarks).any (trigQ tag) = false := by
    rw [List.any_flatMap, List.any_eq_false]
    intro j _; simpa using jobMarks_no_trig j tag
  unfold Workflow.hasTrig
  show (marksOf p).any (trigQ tag) = _
  simp only [marksOf, List.any_append, hf]
  have hfin : (p.fin.map fun j => Mark.finally j.id j.needs).any (trigQ tag) = false := by
    simp [List.any_eq_false, trigQ]
  rw [hfin]
  simp only [any_ite]
  cases tag <;> simp [trigQ] <;> grind

theorem find_ite {α γ : Type} (c : Prop) [Decidable c] (m : α) (q : α → Option γ) :
    (if c then [m] else []).findSome? q = if c then q m else none := by
  split <;> simp_all

theorem marks_split (p : Pipe) :
    (toGha p).marks = ((if p.push = some [] ∧ p.tag ≠ some [] then [Mark.trigPush (if p.pr.isSome = true then 1 else 0)] else []) ++
      (if p.tag = some [] ∧ p.push ≠ some [] then [Mark.trigTag ((if p.pr.isSome = true then 1 else 0) + if p.push.isSome = true then 1 else 0)] else []) ++
      (if (p.all.any fun x => x.manual) = true then [Mark.trigManual] else [])) ++
      p.all.flatMap jobMarks ++ p.fin.map (fun j => Mark.finally j.id j.needs) ++
      (if p.fin.isEmpty = true then [] else [Mark.finallyRoot]) := rfl

theorem art_mark (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup) (j : Core.Job) (hj : j ∈ p.all) :
    (toGha p).artMark j.id =
      if (artifactNeeds j).isEmpty then none else some (artifactNeeds j, (artifactNeeds j).length) := by
  have hnd : p.all.Nodup := List.Nodup.of_map _ hn
  unfold Workflow.artMark
  have key := findSome_flatMap_unique p.all jobMarks (artQ j.id) j hj hnd (by
    intro y hy hne
    have := idne hn hy hj hne
    simp [jobMarks, artQ, this])
  rw [marks_split]
  simp only [List.findSome?_append, key, find_ite]
  have hfin : (p.fin.map fun j => Mark.finally j.id j.needs).findSome? (artQ j.id) = none :=
    findSome_map_none _ _ _ (by simp [artQ])
  rw [hfin]
  by_cases hm : j.manual <;> by_cases hr : j.retry = 0 <;> by_cases he : (artifactNeeds j).isEmpty <;>
    simp [jobMarks, artQ, hm, hr, he, List.findSome?_append, List.findSome?_cons]

theorem retry_mark (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup) (j : Core.Job) (hj : j ∈ p.all) :
    (toGha p).retryMark j.id = if j.retry = 0 then none else some j.retry := by
  have hnd : p.all.Nodup := List.Nodup.of_map _ hn
  unfold Workflow.retryMark
  have key := findSome_flatMap_unique p.all jobMarks (retryQ j.id) j hj hnd (by
    intro y hy hne
    have := idne hn hy hj hne
    simp [jobMarks, retryQ, this])
  rw [marks_split]
  simp only [List.findSome?_append, key, find_ite]
  have hfin : (p.fin.map fun j => Mark.finally j.id j.needs).findSome? (retryQ j.id) = none :=
    findSome_map_none _ _ _ (by simp [retryQ])
  rw [hfin]
  by_cases hm : j.manual <;> by_cases hr : j.retry = 0 <;> by_cases he : (artifactNeeds j).isEmpty <;>
    simp [jobMarks, retryQ, hm, hr, he, List.findSome?_cons]

theorem manual_mark (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup) (j : Core.Job) (hj : j ∈ p.all) :
    (toGha p).hasManual j.id = j.manual := by
  unfold Workflow.hasManual
  rw [marks_split]
  have hf : (p.all.flatMap jobMarks).any (manualQ j.id) = j.manual := by
    rw [Bool.eq_iff_iff, List.any_flatMap, List.any_eq_true]
    constructor
    · rintro ⟨y, hy, hq⟩
      have hq' : y.manual = true ∧ y.id = j.id := by
        have := hq
        simp [jobMarks, manualQ] at this
        exact this
      have : y = j := List.inj_on_of_nodup_map hn hy hj hq'.2
      subst this; exact hq'.1
    · intro h
      exact ⟨j, hj, by simp [jobMarks, manualQ, h]⟩
  have hfin : (p.fin.map fun j => Mark.finally j.id j.needs).any (manualQ j.id) = false := by
    simp [List.any_eq_false, manualQ]
  simp only [List.any_append, hf, hfin, any_ite]
  simp [manualQ]

theorem fin_mark_fin (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup) (j : Core.Job) (hj : j ∈ p.fin) :
    (toGha p).finMark j.id = some j.needs := by
  have hja : j ∈ p.all := by simp [Pipe.all, hj]
  have hnd : p.fin.Nodup := by
    have : p.all.Nodup := List.Nodup.of_map _ hn
    exact (List.nodup_append.mp this).2.1
  unfold Workflow.finMark
  rw [marks_split]
  have hf : (p.all.flatMap jobMarks).findSome? (finQ j.id) = none :=
    findSome_flatMap_none _ _ _ (by
      intro y _; simp [jobMarks, finQ])
  have hfin := findSome_map_unique p.fin (fun j => Mark.finally j.id j.needs) (finQ j.id) j hj hnd (by
    intro y hy hne
    have := idne hn (by simp [Pipe.all, hy]) hja hne
    simp [finQ, this])
  simp only [List.findSome?_append, hf, hfin, find_ite]
  simp [finQ]

theorem fin_mark_main (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup) (j : Core.Job) (hj : j ∈ p.jobs) :
    (toGha p).finMark j.id = none := by
  have hja : j ∈ p.all := by simp [Pipe.all, hj]
  unfold Workflow.finMark
  rw [marks_split]
  have hf : (p.all.flatMap jobMarks).findSome? (finQ j.id) = none :=
    findSome_flatMap_none _ _ _ (by
      intro y _; simp [jobMarks, finQ])
  have hfin : (p.fin.map fun j => Mark.finally j.id j.needs).findSome? (finQ j.id) = none :=
    findSome_map_none _ _ _ (by
      intro y hy
      have hd := (List.nodup_append.mp (List.Nodup.of_map _ hn)).2.2 j hj y hy
      have := idne hn (by simp [Pipe.all, hy]) hja (Ne.symm hd)
      simp [finQ, this])
  simp only [List.findSome?_append, hf, hfin, find_ite]
  simp [finQ]

theorem unwrap_wrap' (n : Nat) (hn : n ≠ 0) (l : List String) :
    unwrapRetry n (retryHead n ++ (l ++ retryTail)) = some l := by
  have := unwrap_wrap n hn l
  rwa [wrapRetry, if_neg hn, List.append_assoc] at this

theorem timeout_rt (t : Nat) : (if t = 0 then none else some t).getD 0 = t := by
  split <;> simp_all

theorem common (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup) (j : Core.Job) (hj : j ∈ p.all)
    (ns : List String) (cond : Option String) :
    collapseRetry (toGha p) (emitJob ns cond j) (collapseCarried (toGha p) (emitJob ns cond j)
      (parseJob (emitJob ns cond j))) =
    { j with needs := ns.map (fun m => ⟨m, decide (m ∈ artifactNeeds j)⟩), manual := false,
             onFailure := false } := by
  have hk : (emitJob ns cond j).key = j.id := rfl
  unfold collapseCarried collapseRetry Workflow.artMark Workflow.retryMark
  rw [hk]
  have ha := art_mark p hn j hj
  have hr := retry_mark p hn j hj
  unfold Workflow.artMark at ha
  unfold Workflow.retryMark at hr
  rw [ha, hr]
  by_cases he : artifactNeeds j = [] <;> by_cases hr0 : j.retry = 0 <;>
    simp [he, hr0, parseJob, emitJob, scriptStep, wrapRetry, Function.comp_def, downloadStep, timeout_rt]
  all_goals exact map_self _ (fun x => by cases x; simp [unwrap_wrap' _ hr0]) j.steps

theorem needs_rt (ns : List Core.Need) (hn : (ns.map Core.Need.job).Nodup) :
    (ns.map Core.Need.job).map (fun m => (⟨m, decide (m ∈ (ns.filter (·.artifacts)).map Core.Need.job)⟩ : Core.Need)) = ns := by
  rw [List.map_map]
  apply map_self_mem
  intro n hmem
  have hiff : n.job ∈ (ns.filter (·.artifacts)).map Core.Need.job ↔ n.artifacts = true := by
    constructor
    · intro h
      obtain ⟨m, hm, hmj⟩ := List.mem_map.mp h
      obtain ⟨hm1, hm2⟩ := List.mem_filter.mp hm
      have : m = n := List.inj_on_of_nodup_map hn hm1 hmem hmj
      subst this; exact hm2
    · intro h
      exact List.mem_map.mpr ⟨n, List.mem_filter.mpr ⟨hmem, h⟩, rfl⟩
  cases n with
  | mk job art =>
    simp only [Function.comp] at hiff ⊢
    cases art <;> simp_all

theorem main_ok (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup)
    (hneed : ∀ j ∈ p.all, (j.needs.map Core.Need.job).Nodup)
    (hon : ∀ j ∈ p.jobs, j.onFailure = false) (j : Core.Job) (hj : j ∈ p.jobs) :
    mainOf (toGha p) (mainJob j) = j := by
  have hja : j ∈ p.all := by simp [Pipe.all, hj]
  have hc := common p hn j hja (j.needs.map (·.job)) (if j.manual then some condDispatch else none)
  have hr := needs_rt j.needs (hneed j hja)
  have hm := manual_mark p hn j hja
  unfold mainOf collapseManual
  have hk : (mainJob j).key = j.id := rfl
  rw [hk, hm]
  show (if j.manual = true then _ else _) = j
  have hc' : collapseRetry (toGha p) (mainJob j) (collapseCarried (toGha p) (mainJob j) (parseJob (mainJob j))) =
      { j with needs := (j.needs.map (·.job)).map (fun m => ⟨m, decide (m ∈ artifactNeeds j)⟩), manual := false,
               onFailure := false } := hc
  rw [hc']
  unfold artifactNeeds
  rw [hr]
  have hf := hon j hj
  clear hc hc' hm hk hr hja
  cases hmj : j.manual <;> simp [mainJob, emitJob, condDispatch, hmj] <;> cases j <;> simp_all

theorem mainKeys_toGha (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup) :
    (toGha p).mainKeys = p.jobs.map (·.id) := by
  unfold Workflow.mainKeys
  show ((p.jobs.map mainJob ++ p.fin.map (finJob (p.jobs.map (·.id)))).filter _).map _ = _
  rw [List.filter_append, List.filter_eq_self.mpr, List.filter_eq_nil_iff.mpr]
  · simp [List.map_map, Function.comp_def, mainJob, finJob, emitJob]
  · intro gj hgj
    obtain ⟨j, hj, rfl⟩ := List.mem_map.mp hgj
    have := fin_mark_fin p hn j hj
    have hk : (finJob (p.jobs.map (·.id)) j).key = j.id := rfl
    simp [hk, this]
  · intro gj hgj
    obtain ⟨j, hj, rfl⟩ := List.mem_map.mp hgj
    have := fin_mark_main p hn j hj
    have hk : (mainJob j).key = j.id := rfl
    simp [hk, this]

theorem fin_ok (p : Pipe) (hn : (p.all.map Core.Job.id).Nodup)
    (hneed : ∀ j ∈ p.fin, ∀ n ∈ j.needs, n.job ∈ p.jobs.map Core.Job.id)
    (hman : ∀ j ∈ p.fin, j.manual = false) (j : Core.Job) (hj : j ∈ p.fin) :
    finOf (toGha p) (finJob (p.jobs.map (·.id)) j) = some (j.onFailure, j.needs) ∧
    finishFin (toGha p) (finJob (p.jobs.map (·.id)) j) j.onFailure j.needs = j := by
  have hja : j ∈ p.all := by simp [Pipe.all, hj]
  have hfm := fin_mark_fin p hn j hj
  constructor
  · unfold finOf
    have hk : (finJob (p.jobs.map (·.id)) j).key = j.id := rfl
    rw [hk, hfm]
    cases hof : j.onFailure <;> simp [finJob, emitJob, condAlways, condAlwaysFailure, hof]
  · have hc := common p hn j hja (p.jobs.map (·.id)) (some (if j.onFailure then condAlwaysFailure else condAlways))
    have hk : (finJob (p.jobs.map (·.id)) j).key = j.id := rfl
    have hmk := mainKeys_toGha p hn
    unfold finishFin
    have hc' : collapseRetry (toGha p) (finJob (p.jobs.map (·.id)) j)
        (collapseCarried (toGha p) (finJob (p.jobs.map (·.id)) j) (parseJob (finJob (p.jobs.map (·.id)) j))) =
        { j with needs := (p.jobs.map (·.id)).map (fun m => ⟨m, decide (m ∈ artifactNeeds j)⟩), manual := false,
                 onFailure := false } := hc
    simp only [hc', hmk]
    have hsame : sameSet (p.jobs.map (·.id) ++ j.needs.map (·.job))
        (((p.jobs.map (·.id)).map (fun m => (⟨m, decide (m ∈ artifactNeeds j)⟩ : Core.Need))).map (·.job)) = true := by
      simp only [List.map_map, Function.comp_def, List.map_id']
      unfold sameSet
      simp only [Bool.and_eq_true, List.all_eq_true, List.contains_iff_mem, decide_eq_true_eq]
      refine ⟨?_, fun a ha => by simp [ha]⟩
      intro a ha
      rcases List.mem_append.mp ha with h | h
      · exact h
      · obtain ⟨n, hn1, rfl⟩ := List.mem_map.mp h
        exact hneed j hj n hn1
    rw [if_pos hsame]
    have := hman j hj
    clear hc hc' hk hmk hsame hfm hja
    cases j
    simp_all

theorem parseOnPush_toGha (p : Pipe) :
    parseOnPush (toGha p) = (p.push, p.tag) := by
  have ht := hasTrig_toGha p true
  have hf := hasTrig_toGha p false
  unfold parseOnPush
  show (match (toOn p).push with | none => (none, none) | some (none, none) => (some [], some []) | some (b, t) => _) = _
  simp only [toOn, ht, hf]
  rcases hp : p.push with _ | _ | ⟨b, bs⟩ <;> rcases htg : p.tag with _ | _ | ⟨t, ts⟩ <;>
    simp [pushBranches, pushTags, hp, htg, matchAll]

theorem jobs_eq (p : Pipe) (hw : Wf p) :
    (toGha p).jobs.filterMap (mainPick (toGha p)) = p.jobs := by
  obtain ⟨_, hnd, _, _, hn1, hn2, hneed, _, _, hon, hman⟩ := hw
  have hn : (p.all.map Core.Job.id).Nodup := hnd
  show (p.jobs.map mainJob ++ p.fin.map (finJob (p.jobs.map (·.id)))).filterMap _ = _
  rw [List.filterMap_append, List.filterMap_map, List.filterMap_map]
  have h1 : List.filterMap (mainPick (toGha p) ∘ mainJob) p.jobs = p.jobs := by
    refine (List.filterMap_congr (g := some) ?_).trans List.filterMap_some
    intro j hj
    have hk : (mainJob j).key = j.id := rfl
    have hfm : finOf (toGha p) (mainJob j) = none := by
      unfold finOf; rw [hk, fin_mark_main p hn j hj]
    simp [Function.comp, mainPick, hfm, main_ok p hn hneed hon j hj]
  have h2 : List.filterMap (mainPick (toGha p) ∘ finJob (p.jobs.map (·.id))) p.fin = [] := by
    rw [List.filterMap_eq_nil_iff]
    intro j hj
    have := (fin_ok p hn hn2 hman j hj).1
    simp [Function.comp, mainPick, this]
  rw [h1, h2]; simp

theorem fin_eq (p : Pipe) (hw : Wf p) :
    (toGha p).jobs.filterMap (finPick (toGha p)) = p.fin := by
  obtain ⟨_, hnd, _, _, hn1, hn2, hneed, _, _, hon, hman⟩ := hw
  have hn : (p.all.map Core.Job.id).Nodup := hnd
  show (p.jobs.map mainJob ++ p.fin.map (finJob (p.jobs.map (·.id)))).filterMap _ = _
  rw [List.filterMap_append, List.filterMap_map, List.filterMap_map]
  have h1 : List.filterMap (finPick (toGha p) ∘ mainJob) p.jobs = [] := by
    rw [List.filterMap_eq_nil_iff]
    intro j hj
    have hk : (mainJob j).key = j.id := rfl
    have hfm : finOf (toGha p) (mainJob j) = none := by
      unfold finOf; rw [hk, fin_mark_main p hn j hj]
    simp [Function.comp, finPick, hfm]
  have h2 : List.filterMap (finPick (toGha p) ∘ finJob (p.jobs.map (·.id))) p.fin = p.fin := by
    refine (List.filterMap_congr (g := some) ?_).trans List.filterMap_some
    intro j hj
    obtain ⟨h1, h2⟩ := fin_ok p hn hn2 hman j hj
    simp [Function.comp, finPick, h1, h2]
  rw [h1, h2]; simp

theorem rawGha_toGha (p : Pipe) (hw : Wf p) : rawGha (toGha p) = p := by
  have hpush := parseOnPush_toGha p
  unfold rawGha
  rw [jobs_eq p hw, fin_eq p hw, hpush]
  cases p; rfl

theorem gha_from_to (p : Pipe) (h : GhaFrag p) : fromGha (toGha p) = p := by
  unfold fromGha
  rw [restore_empty]
  exact rawGha_toGha p h.1

end Gha
end Pipemaster
