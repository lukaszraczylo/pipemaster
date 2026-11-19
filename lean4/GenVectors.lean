import Lean.Data.Json
import Pipemaster

open Lean (Json)
open Pipemaster

/-! Emits bounded-exhaustive differential vectors from the executable defs of the model.
Object keys come out sorted (Lean.Json), case order is fixed by the enumerations below. -/

namespace GenVectors

def levelName : Level → String
  | .native => "native"
  | .emulated => "emulated"
  | .external => "external"
  | .lost => "lost"

def allLevels : List Level := [.native, .emulated, .external, .lost]

/-- All subsets of xs of size at most k, in a fixed order. -/
def subsetsUpTo : List α → Nat → List (List α)
  | [], _ => [[]]
  | x :: t, k =>
    subsetsUpTo t k ++ (match k with
      | 0 => []
      | k' + 1 => (subsetsUpTo t k').map (x :: ·))

/-- One pick from each option list. -/
def sequence : List (List γ) → List (List γ)
  | [] => [[]]
  | os :: rest => os.flatMap fun o => (sequence rest).map (o :: ·)

/-- Lists of length 0 to k over the pool, repeats allowed. -/
def listsUpTo (pool : List α) : Nat → List (List α)
  | 0 => [[]]
  | k + 1 =>
    let prev := listsUpTo pool k
    prev ++ (prev.filter (·.length == k)).flatMap fun l => pool.map (l ++ [·])

def emit (theorem_ : String) (cases : List (Json × Json)) (envs : Option Json := none) : String :=
  let lines := cases.map fun (i, o) => (Json.mkObj [("in", i), ("out", o)]).compress
  let envPart := match envs with
    | some e => s!"\"envs\":{e.compress},"
    | none => ""
  s!"\{\"theorem\":\"{theorem_}\",{envPart}\"cases\":[\n" ++ ",\n".intercalate lines ++ "\n]}\n"

/-! ## DAG -/

def needJson (n : Need) : Json :=
  Json.mkObj [("job", n.jobId), ("optional", n.optional)]

def jobJson (j : Job) : Json :=
  Json.mkObj [("id", j.id), ("needs", Json.arr (j.needs.map needJson).toArray)]

def dagOut (js : List Job) : Json :=
  match Dag.layersE js with
  | .ok ls => Json.arr (ls.map fun l => Json.arr (l.map Json.str).toArray).toArray
  | .error .cycle => Json.mkObj [("error", "cycle")]
  | .error .unknownNeed => Json.mkObj [("error", "unknown_need")]

def dagCase (js : List Job) : Json × Json :=
  (Json.arr (js.map jobJson).toArray, dagOut js)

def req (x : String) : Need := ⟨x, false⟩
def opt (x : String) : Need := ⟨x, true⟩

def nameOf (i : Nat) : String := ["a", "b", "c", "d"].getD i "?"

def mkJobs (ids : List String) (needs : List (List Need)) : List Job :=
  (ids.zip needs).map fun (i, n) => ⟨i, n⟩

/-- Every graph on n jobs where each need is required: all edge subsets, self-loops included. -/
def denseGraphs (n : Nat) : List (List Job) :=
  let ids := (List.range n).map nameOf
  let pairs := ids.flatMap fun s => ids.map fun d => (s, d)
  (subsetsUpTo pairs (n * n)).map fun es =>
    mkJobs ids (ids.map fun s => (es.filter (·.1 == s)).map fun e => req e.2)

/-- Four jobs, at most three required edges in total. -/
def fourJobsFewEdges : List (List Job) :=
  let ids := (List.range 4).map nameOf
  let pairs := ids.flatMap fun s => ids.map fun d => (s, d)
  (subsetsUpTo pairs 3).map fun es =>
    mkJobs ids (ids.map fun s => (es.filter (·.1 == s)).map fun e => req e.2)

/-- Four jobs, each needing at most one job (functional graphs: chains, forks, cycles). -/
def fourJobsOneNeed : List (List Job) :=
  let ids := (List.range 4).map nameOf
  let opts : List (List Need) := [[]] ++ ids.map fun d => [req d]
  (sequence (ids.map fun _ => opts)).map (mkJobs ids)

/-- Four jobs: forward edges only (acyclic, up to depth four), alone and with one back edge. -/
def fourJobsForward : List (List Job) :=
  let ids := (List.range 4).map nameOf
  let fwd := [("a","b"),("a","c"),("a","d"),("b","c"),("b","d"),("c","d")]
  let back := [("b","a"),("c","a"),("d","a"),("c","b"),("d","b"),("d","c")]
  let build (es : List (String × String)) :=
    mkJobs ids (ids.map fun s => (es.filter (·.1 == s)).map fun e => req e.2)
  let subs := subsetsUpTo fwd 6
  subs.map build ++ subs.flatMap fun fs => back.map fun b => build (fs ++ [b])

/-- Required and optional needs, including on the missing job z. -/
def missingNeeds : List (List Job) :=
  let kinds (ids : List String) : List Need := (ids ++ ["z"]).flatMap fun t => [req t, opt t]
  let build (n : Nat) (k : Nat) :=
    let ids := (List.range n).map nameOf
    (sequence (ids.map fun _ => subsetsUpTo (kinds ids) k)).map (mkJobs ids)
  build 1 2 ++ build 2 2 ++ build 3 1

/-- Duplicate job ids (first wins), with needs on both ids and on a missing job. -/
def duplicateIds : List (List Job) :=
  let idSeqs (n : Nat) : List (List String) :=
    sequence ((List.range n).map fun _ => ["a", "b"])
  let kinds : List Need := ["a", "b", "z"].flatMap fun t => [req t, opt t]
  let opts : List (List Need) := subsetsUpTo kinds 1
  [1, 2, 3].flatMap fun n =>
    (idSeqs n).flatMap fun ids =>
      (sequence (ids.map fun _ => opts)).map (mkJobs ids)

def dagCases : List (Json × Json) :=
  ((denseGraphs 0 ++ denseGraphs 1 ++ denseGraphs 2 ++ denseGraphs 3
    ++ fourJobsFewEdges ++ fourJobsOneNeed ++ fourJobsForward
    ++ missingNeeds ++ duplicateIds).map dagCase)

/-! ## Matrix -/

def comboJson (c : Combo) : Json :=
  Json.arr (c.map fun (k, v) => Json.arr #[Json.str k, Json.str v]).toArray

def matrixJson (m : Matrix) : Json :=
  Json.mkObj [
    ("axes", Json.arr (m.axes.map fun a =>
      Json.mkObj [("name", a.name), ("values", Json.arr (a.values.map Json.str).toArray)]).toArray),
    ("include", Json.arr (m.includes.map comboJson).toArray),
    ("exclude", Json.arr (m.excludes.map comboJson).toArray)]

def matrixCase (m : Matrix) : Json × Json :=
  (matrixJson m, Json.arr ((Matrix.expand m).map comboJson).toArray)

def valueLists : List (List String) := [[], ["1"], ["1", "2"], ["1", "2", "3"]]

/-- No axes, one axis (k1), or two axes (k1, k2), each with every value list. -/
def axisConfigs : List (List Axis) :=
  [[]]
  ++ valueLists.map (fun v => [⟨"k1", v⟩])
  ++ valueLists.flatMap fun v1 => valueLists.map fun v2 => [⟨"k1", v1⟩, ⟨"k2", v2⟩]

def repConfigs : List (List Axis) :=
  [[], [⟨"k1", ["1", "2"]⟩], [⟨"k1", ["1", "2"]⟩, ⟨"k2", ["1", "2"]⟩]]

/-- Combos over keys k1..k3 (k3 is no axis) and values 1, 2; two with a key repeated inside one entry. -/
def comboPool : List Combo :=
  let keys := ["k1", "k2", "k3"]
  let vals := ["1", "2"]
  [[]]
  ++ keys.flatMap (fun k => vals.map fun v => [(k, v)])
  ++ [("k1", "k2"), ("k1", "k3"), ("k2", "k3")].flatMap (fun (a, b) =>
      vals.flatMap fun v => vals.map fun w => [(a, v), (b, w)])
  ++ [[("k1", "1"), ("k1", "2")], [("k1", "2"), ("k1", "1")]]

def smallPool : List Combo :=
  [[("k1", "1")], [("k2", "2")], [("k1", "2"), ("k3", "1")], [("k3", "2")]]

def mk (axes : List Axis) (inc exc : List Combo) : Matrix := ⟨axes, inc, exc⟩

def matrixCases : List (Json × Json) :=
  let one := listsUpTo comboPool 1
  let two := listsUpTo comboPool 2
  let sm := listsUpTo smallPool 2
  let sets : List Matrix :=
    -- excludes only, includes only, over every axis configuration
    axisConfigs.flatMap (fun ax => one.map fun e => mk ax [] e)
    ++ axisConfigs.flatMap (fun ax => one.map fun i => mk ax i [])
    -- two includes (a key repeated across includes overwrites), two excludes
    ++ repConfigs.flatMap (fun ax => two.map fun i => mk ax i [])
    ++ (repConfigs.drop 1).flatMap (fun ax => two.map fun e => mk ax [] e)
    -- one include with one exclude
    ++ repConfigs.flatMap (fun ax => one.flatMap fun i => one.map fun e => mk ax i e)
    -- two includes with two excludes over a small pool
    ++ sm.flatMap (fun i => sm.map fun e => mk (repConfigs.getD 2 []) i e)
  sets.map matrixCase

/-! ## Ledger -/

def useJson (u : FeatureUse) : Json :=
  Json.mkObj [("ir_path", u.irPath), ("feature", u.feature)]

def supportJson (s : Support) : Json :=
  Json.mkObj [("level", levelName s.level), ("strategy", s.strategy), ("caveat", s.caveat)]

def opJson : Ledger.Op → Json
  | .decide u => Json.mkObj [("op", "decide"), ("use", useJson u)]
  | .note u s d =>
    Json.mkObj [("op", "note"), ("use", useJson u), ("support", supportJson s), ("detail", d)]

def entryJson (e : Ledger.Entry) : Json :=
  Json.mkObj [("ir_path", e.use.irPath), ("feature", e.use.feature), ("level", levelName e.level),
    ("strategy", e.strategy), ("detail", e.detail)]

def u1 : FeatureUse := ⟨"p1", "f"⟩
def u2 : FeatureUse := ⟨"p2", "g"⟩
def u3 : FeatureUse := ⟨"p3", "f"⟩

/-- Matrix variants for feature f: each level, and absent (lost by lookup). g is fixed at external. -/
def ledgerMatrices : List CapsMatrix :=
  let g : Feature × Support := ("g", ⟨.external, "gs", "gc"⟩)
  (allLevels.map fun l => [("f", ⟨l, s!"fs-{levelName l}", s!"fc-{levelName l}"⟩), g]) ++ [[g]]

def matrixJsonL (m : CapsMatrix) : Json :=
  Json.arr (m.map fun (f, s) => Json.mkObj [("feature", f), ("support", supportJson s)]).toArray

/-- Op alphabet: decide on either use, note on u1 at every level, note on u2 at one level.
Strategy and detail carry the sequence position so a replacing Note is observable. -/
def opsAt (pos : Nat) : List Ledger.Op :=
  let note (u : FeatureUse) (l : Level) : Ledger.Op :=
    .note u ⟨l, s!"ns-{levelName l}-{pos}", ""⟩ s!"nd-{pos}"
  [.decide u1, .decide u2]
  ++ allLevels.map (note u1)
  ++ [note u2 .emulated]

def ledgerCases : List (Json × Json) :=
  let seqs : List (List Ledger.Op) :=
    [[]] ++ (opsAt 0).map (fun a => [a])
    ++ (opsAt 0).flatMap (fun a => (opsAt 1).map fun b => [a, b])
    ++ (opsAt 0).flatMap (fun a => (opsAt 1).flatMap fun b => (opsAt 2).map fun c => [a, b, c])
  let uses := [u1, u2, u3]
  ledgerMatrices.flatMap fun caps =>
    seqs.map fun ops =>
      let r := Ledger.run caps ops
      let un := Ledger.unaccounted caps ops uses
      (Json.mkObj [("matrix", matrixJsonL caps), ("ops", Json.arr (ops.map opJson).toArray),
          ("uses", Json.arr (uses.map useJson).toArray)],
        Json.mkObj [("emitted", Json.arr (r.emitted.map useJson).toArray),
          ("report", Json.arr (r.report.map entryJson).toArray),
          ("unaccounted", Json.arr (un.map useJson).toArray)])

/-! ## Expression translation

Bounded-exhaustive over a fixed alphabet. Depth 1 is every comparison, affix test and bare atom over
the refs and literals below; depth 2 negates every depth-1 expression and joins every pair of the core
predicates with and / or; depth 3 negates a hand-picked sample of depth-2 expressions and joins every
pair of the pivot expressions. Each case carries the truthiness of source and target on all envs. -/

open Expr Translate in
def exprJson : E → Json
  | .null => Json.arr #["null"]
  | .bool b => Json.arr #["bool", b]
  | .str s => Json.arr #["str", s]
  | .num n => Json.arr #["num", Lean.toJson n]
  | .ref r => Json.arr #["ref", refName r]
  | .not x => Json.arr #["not", exprJson x]
  | .and l r => Json.arr #["and", exprJson l, exprJson r]
  | .or l r => Json.arr #["or", exprJson l, exprJson r]
  | .eq l r => Json.arr #["eq", exprJson l, exprJson r]
  | .ne l r => Json.arr #["ne", exprJson l, exprJson r]
  | .lt l r => Json.arr #["lt", exprJson l, exprJson r]
  | .startsWith a b => Json.arr #["startsWith", exprJson a, exprJson b]
  | .endsWith a b => Json.arr #["endsWith", exprJson a, exprJson b]
  | .contains a b => Json.arr #["contains", exprJson a, exprJson b]
  | .call n x => Json.arr #["call", n, exprJson x]
  | .rx x m s => Json.arr #["rx", exprJson x, modeName m, s]
  | .nrx x m s => Json.arr #["nrx", exprJson x, modeName m, s]
  | .rawRx x re => Json.arr #["rawRx", exprJson x, re]
  | .opq => Json.arr #["opq"]
where
  refName : Ref → String
    | .refName => "ref_name" | .refType => "ref_type" | .event => "event" | .sha => "sha"
    | .branch => "branch" | .tag => "tag" | .ghaNative => "gha_native" | .gitlabNative => "gitlab_native"
  modeName : Mode → String
    | .exact => "exact" | .prefix => "prefix" | .suffix => "suffix" | .sub => "sub"

def optJson (o : Option String) : Json := match o with | some s => Json.str s | none => Json.null

def envJson (e : Expr.Env) : Json :=
  Json.mkObj [("ref_name", optJson e.refName), ("ref_type", optJson (e.refType.map Expr.rtName)),
    ("event", optJson (e.event.map Expr.evGha)), ("sha", optJson e.sha), ("native", optJson e.nat)]

/-- Seven environments: unset, empty strings, a branch, case variants, a tag named like the branch,
a branch with no name, and a tag with a slash name. -/
def exprEnvs : List Expr.Env :=
  [⟨none, none, none, none, none⟩,
   ⟨some "", some .branch, some .push, some "", some ""⟩,
   ⟨some "main", some .branch, some .push, some "abc", some "x"⟩,
   ⟨some "MAIN", some .branch, some .pullRequest, some "ABC", some "X"⟩,
   ⟨some "main", some .tag, some .push, some "abc", none⟩,
   ⟨none, some .branch, some .workflowDispatch, none, none⟩,
   ⟨some "release/1", some .tag, some .schedule, some "0", some "0"⟩]

def boolsJson (bs : List Bool) : Json := Json.arr (bs.map Json.bool).toArray

open Expr Translate in
def exprCase (dir : String) (e : E) : Json × Json :=
  let fwd := dir == "gha-gitlab"
  let res := if fwd then tr e else trR e
  let srcSem : Sem := if fwd then .gha else .gitlab
  let tgtSem : Sem := if fwd then .gitlab else .gha
  let view (sem : Sem) (env : Env) : View := if sem == .gha then ghaView env else mapEnv env
  let out := match res with
    | none => Json.mkObj [("tr", Json.null)]
    | some t => Json.mkObj [("tr", exprJson t),
        ("src", boolsJson (exprEnvs.map fun env => holds srcSem (view srcSem env) e)),
        ("tgt", boolsJson (exprEnvs.map fun env => holds tgtSem (view tgtSem env) t))]
  (Json.mkObj [("dir", dir), ("expr", exprJson e)], out)

open Expr in
def lits (ss : List String) : List E := ss.map E.str

open Expr in
/-- Depth-1 expressions over the refs and literals. -/
def depth1 (refs : List Ref) (ls : List E) (extra : List E) : List E :=
  let rs := refs.map E.ref
  let pairs := rs.flatMap (fun a => ls.map fun b => (a, b)) ++ ls.flatMap (fun a => rs.map fun b => (a, b))
    ++ rs.flatMap (fun a => rs.map fun b => (a, b))
  let rl := rs.flatMap (fun a => ls.map fun b => (a, b))
  rs ++ ls ++ pairs.flatMap (fun (a, b) => [E.eq a b, E.ne a b, E.lt a b])
    ++ rl.flatMap (fun (a, b) => [E.startsWith a b, E.endsWith a b, E.contains a b])
    ++ extra

open Expr in
def joinAll (xs ys : List E) : List E :=
  xs.flatMap fun a => ys.flatMap fun b => [E.and a b, E.or a b]

open Expr in
def layers (p1 core pivots : List E) : List E :=
  let d2 := p1.map E.not ++ joinAll core core
  p1 ++ d2 ++ pivots.map E.not ++ joinAll pivots pivots

open Expr in
def ghaCases : List (Json × Json) :=
  let refs := [Ref.refName, .refType, .event, .sha, .ghaNative]
  let ls := lits ["main", "MAIN", "", "push", "branch", "pull_request", "0", ".", "a/b", "a\\"] ++ [E.num 1, E.null]
  let rn := E.ref .refName
  let rt := E.ref .refType
  let ev := E.ref .event
  let p1 := depth1 refs ls [E.ref .branch, E.ref .gitlabNative, E.call "toJSON" rn, E.opq, E.bool true]
  let guard := E.eq rt (E.str "branch")
  let core := [rn, E.ref .sha, ev, rt, E.ref .ghaNative,
    E.eq rn (E.str "main"), E.eq (E.str "MAIN") rn, E.ne rn (E.str "main"), E.eq rn (E.str ""),
    E.eq rn (E.str "0"), E.eq rn (E.ref .sha), E.eq ev (E.str "push"), E.eq ev (E.str "pull_request"),
    E.ne ev (E.str "push"), guard, E.eq (E.str "tag") rt, E.startsWith rn (E.str "main"),
    E.endsWith rn (E.str "main"), E.contains rn (E.str "ain"), E.contains rn (E.str "a"),
    E.lt rn (E.str "main"), E.eq rn E.null, E.eq (E.ref .ghaNative) (E.str "main"), E.call "toJSON" rn]
  let pivots := [rn, guard, E.eq rn (E.str "main"), E.ne rn (E.str "main"),
    E.and guard (E.eq rn (E.str "main")), E.and guard (E.startsWith rn (E.str "rel")),
    E.and guard (E.ne rn (E.str "main")), E.eq ev (E.str "push"), E.not (E.eq rn (E.str "main")),
    E.or (E.eq rn (E.str "main")) (E.ref .sha), E.contains rn (E.str "a"), E.opq]
  (layers p1 core pivots).map (exprCase "gha-gitlab")

open Expr in
def gitlabCases : List (Json × Json) :=
  let refs := [Ref.refName, .branch, .tag, .event, .sha, .gitlabNative, .refType]
  let rn := E.ref .refName
  let br := E.ref .branch
  let ev := E.ref .event
  let ss := ["main", "MAIN", "push", "merge_request_event", "web", "0", "", "a", ".", "a/b"]
  let rxs := refs.flatMap fun r => [Mode.exact, .prefix, .suffix, .sub].flatMap fun m =>
    ss.flatMap fun s => [E.rx (E.ref r) m s, E.nrx (E.ref r) m s]
  let eqs := refs.flatMap fun r => (ss.map E.str ++ [E.null]).flatMap fun l =>
    [E.eq (E.ref r) l, E.ne (E.ref r) l]
  let p1 := refs.map E.ref ++ [E.null, E.str "main", E.num 1] ++ rxs ++ eqs
    ++ refs.map (fun r => E.rawRx (E.ref r) "ab.*") ++ [E.opq, E.rx (E.str "main") .exact "main"]
  let core := [rn, br, E.ref .tag, ev, E.ref .gitlabNative, E.rx rn .exact "main", E.rx rn .exact "MAIN",
    E.nrx rn .exact "main", E.rx br .exact "main", E.rx (E.ref .tag) .exact "main", E.rx br .prefix "main",
    E.rx rn .suffix "main", E.rx rn .sub "ai", E.rx rn .sub "a", E.rx ev .exact "push",
    E.rx ev .exact "merge_request_event", E.rx ev .exact "web", E.rx ev .prefix "push",
    E.eq rn (E.str "main"), E.ne br (E.str "main"), E.rx rn .exact "0", E.rawRx rn "ab.*",
    E.rx (E.ref .sha) .exact "main"]
  let pivots := [rn, br, E.rx rn .exact "main", E.nrx rn .exact "main", E.rx br .exact "main",
    E.nrx br .exact "main", E.rx ev .exact "push", E.eq rn (E.str "main"), E.rx rn .sub "a",
    E.not (E.rx rn .exact "main"), E.or (E.rx rn .exact "main") (E.ref .sha), E.opq]
  (layers p1 core pivots).map (exprCase "gitlab-gha")

def exprCases : List (Json × Json) := ghaCases ++ gitlabCases

def exprEnvsJson : Json := Json.arr (exprEnvs.map envJson).toArray


/-! ## Adapter vectors (Task 23)

Core pipelines are enumerated bounded-exhaustively and kept when they lie in the fragments:
- triggers: every push, tag, pr combination over {absent, no filter, [x, y]} with one plain and one manual job;
- one job: 17 single-feature and combined variants;
- two main jobs: 6 variants each, with needs none, plain, or artifacts (108 cases);
- finally: 4 main layouts (none, a, a and b in parallel, chain a then b) times 8 finally layouts;
- pipeline name and ID: three cases.
At most 2 main and 2 finally jobs, 2 steps per job. -/

open Pipemaster.Core in
def vJson (v : Var) : Json := Json.mkObj [("name", v.name), ("value", v.value)]

def strsJson (l : List String) : Json := Json.arr (l.map Json.str).toArray

def optNat (o : Option Nat) : Json := match o with | some n => Lean.toJson n | none => Json.null

def optStrs (o : Option (List String)) : Json := match o with | some l => strsJson l | none => Json.null

def optBool (o : Option Bool) : Json := match o with | some b => Json.bool b | none => Json.null

open Pipemaster.Core in
def varsJson (l : List Var) : Json := Json.arr (l.map vJson).toArray

open Pipemaster.Core in
def needJ (n : Core.Need) : Json := Json.mkObj [("job", n.job), ("artifacts", n.artifacts)]

open Pipemaster.Core in
def coreStepJson (s : Core.Step) : Json :=
  Json.mkObj [("id", s.id), ("name", s.name), ("script", strsJson s.script), ("env", varsJson s.env)]

open Pipemaster.Core in
def coreJobJson (j : Core.Job) : Json :=
  Json.mkObj [("id", j.id), ("name", j.name), ("needs", Json.arr (j.needs.map needJ).toArray),
    ("env", varsJson j.env), ("runner", strsJson j.runner),
    ("steps", Json.arr (j.steps.map coreStepJson).toArray), ("retry", Lean.toJson j.retry),
    ("timeout_min", Lean.toJson j.timeout), ("manual", j.manual), ("allow_failure", j.allowFailure),
    ("on_failure", j.onFailure)]

def filterJson (f : Option (List String)) : Json :=
  Json.mkObj [("set", f.isSome), ("include", strsJson (f.getD []))]

open Pipemaster.Core in
def coreJson (p : Core.Pipe) : Json :=
  Json.mkObj [("id", p.id), ("name", p.name), ("push", filterJson p.push), ("tag", filterJson p.tag),
    ("pr", filterJson p.pr), ("jobs", Json.arr (p.jobs.map coreJobJson).toArray),
    ("finally", Json.arr (p.fin.map coreJobJson).toArray)]

open Pipemaster.Gha in
def ghaStepJson (s : Gha.Step) : Json :=
  Json.mkObj [("id", s.id), ("name", s.name), ("uses", optionalStr s.uses), ("run", strsJson s.run),
    ("env", varsJson s.env)]
where optionalStr (o : Option String) : Json := match o with | some x => Json.str x | none => Json.null

open Pipemaster.Gha in
def ghaJobJson (j : Gha.Job) : Json :=
  Json.mkObj [("key", j.key), ("name", j.name), ("needs", strsJson j.needs),
    ("cond", match j.cond with | some c => Json.str c | none => Json.null),
    ("runs_on", strsJson j.runsOn), ("timeout", optNat j.timeout), ("continue_on_error", j.continueOnError),
    ("env", varsJson j.env), ("steps", Json.arr (j.steps.map ghaStepJson).toArray)]

open Pipemaster.Gha in
def ghaMarkJson : Gha.Mark → Json
  | .trigPush i => Json.mkObj [("kind", "trig_push"), ("idx", Lean.toJson i)]
  | .trigTag i => Json.mkObj [("kind", "trig_tag"), ("idx", Lean.toJson i)]
  | .trigManual => Json.mkObj [("kind", "trig_manual")]
  | .jobManual j => Json.mkObj [("kind", "job_manual"), ("job", j)]
  | .jobRetry j n => Json.mkObj [("kind", "job_retry"), ("job", j), ("attempts", Lean.toJson n)]
  | .jobNeedsArt j ns pre =>
    Json.mkObj [("kind", "job_needs_art"), ("job", j), ("needs", strsJson ns), ("pre", Lean.toJson pre)]
  | .finally j ns =>
    Json.mkObj [("kind", "finally"), ("job", j), ("need_list", Json.arr (ns.map needJ).toArray)]
  | .finallyRoot => Json.mkObj [("kind", "finally_root")]

open Pipemaster.Gha in
/-- The sort key both sides use for the marker list: kind, job, index. -/
def ghaMarkKey : Gha.Mark → String
  | .trigPush i => "trig_push\u0000\u0000" ++ toString i
  | .trigTag i => "trig_tag\u0000\u0000" ++ toString i
  | .trigManual => "trig_manual\u0000\u0000"
  | .jobManual j => "job_manual\u0000" ++ j ++ "\u0000"
  | .jobRetry j _ => "job_retry\u0000" ++ j ++ "\u0000"
  | .jobNeedsArt j _ _ => "job_needs_art\u0000" ++ j ++ "\u0000"
  | .finally j _ => "finally\u0000" ++ j ++ "\u0000"
  | .finallyRoot => "finally_root\u0000\u0000"

open Pipemaster.Gha in
def ghaJson (g : Gha.Workflow) : Json :=
  let marks := g.marks.mergeSort fun a b => decide (ghaMarkKey a ≤ ghaMarkKey b)
  Json.mkObj [("id", g.id), ("name", g.name),
    ("on", Json.mkObj [("pr", optStrs g.on.pr),
      ("push", match g.on.push with
        | some (b, t) => Json.mkObj [("branches", optStrs b), ("tags", optStrs t)]
        | none => Json.null),
      ("dispatch", g.on.dispatch)]),
    ("jobs", Json.arr (g.jobs.map ghaJobJson).toArray),
    ("marks", Json.arr (marks.map ghaMarkJson).toArray)]

open Pipemaster.Gitlab in
def glBlockJson (b : Gitlab.Block) : Json :=
  Json.mkObj [("id", b.id), ("env", varsJson b.env), ("lines", strsJson b.lines)]

open Pipemaster.Gitlab in
def glJobJson (j : Gitlab.Job) : Json :=
  Json.mkObj [("key", j.key), ("stage", match j.stage with | some s => Json.str s | none => Json.null),
    ("tags", strsJson j.tags), ("needs", optStrs j.needs), ("variables", varsJson j.vars),
    ("script", Json.arr (j.script.map glBlockJson).toArray), ("retry", optNat j.retry),
    ("timeout", optNat j.timeout), ("allow_failure", optBool j.allowFailure),
    ("when", match j.whenKey with | some s => Json.str s | none => Json.null)]

open Pipemaster.Gitlab in
def gitlabJson (g : Gitlab.Pipeline) : Json :=
  let trig (m : Gitlab.TrigMark) : Json :=
    Json.mkObj [("idx", Lean.toJson m.idx), ("kind", m.kind), ("include", strsJson m.incl)]
  Json.mkObj [("stages", strsJson g.stages),
    ("rules", match g.rules with | some rs => Json.arr (rs.map trig).toArray | none => Json.null),
    ("jobs", Json.arr (g.jobs.map glJobJson).toArray), ("marks", Json.arr (g.marks.map trig).toArray)]

section Adapters
open Pipemaster.Core

def cstep (id : String) (script : List String) : Core.Step := ⟨id, "", script, []⟩

def cj (id : String) : Core.Job :=
  ⟨id, "", [], [], ["r1"], [cstep "s1" ["echo 1"]], 0, 0, false, false, false⟩

def twoSteps : List Core.Step :=
  [cstep "s1" ["echo 1", "echo 2"], { cstep "s2" ["echo 3"] with name := "n", env := [⟨"S", "x"⟩] }]

def sinkJob (id : String) : Core.Job :=
  { cj id with name := "Sink", runner := ["r1", "r2"], env := [⟨"K", "v"⟩, ⟨"L", "a b"⟩], steps := twoSteps,
               retry := 2, timeout := 5, manual := true, allowFailure := true }

/-- Single-feature variants of a main job, then a combination of all features. -/
def jobVariants (id : String) : List Core.Job :=
  let b := cj id
  [b, { b with name := "Build" }, { b with runner := ["r1", "r2"] }, { b with env := [⟨"K", "v"⟩] },
   { b with env := [⟨"K", "v"⟩, ⟨"L", "a b"⟩] }, { b with steps := twoSteps },
   { b with steps := [{ cstep "s1" ["echo 1"] with env := [⟨"S", "x"⟩] }] },
   { b with steps := [{ cstep "s1" ["echo 1"] with env := [⟨"S", "q\"b\\c"⟩, ⟨"T", ""⟩] }] },
   { b with steps := [{ cstep "s1" ["echo 1"] with name := "one" }] },
   { b with timeout := 30 }, { b with timeout := 90 }, { b with retry := 1 }, { b with retry := 3 },
   { b with allowFailure := true }, { b with manual := true }, { b with manual := true, allowFailure := true },
   sinkJob id]

def smallVariants (id : String) : List Core.Job :=
  let b := cj id
  [b, { b with retry := 2 }, { b with timeout := 30 }, { b with manual := true },
   { b with env := [⟨"K", "v"⟩] }, { b with allowFailure := true }]

def pipeOf (jobs fin : List Core.Job) (push tag pr : Option (List String) := none) : Core.Pipe :=
  ⟨"ci", "", push, tag, pr, jobs, fin⟩

def pushAll (jobs fin : List Core.Job) : Core.Pipe := pipeOf jobs fin (some [])

def triggerFilters : List (Option (List String)) := [none, some [], some ["x", "y"]]

def triggerPipes : List Core.Pipe :=
  triggerFilters.flatMap fun push => triggerFilters.flatMap fun tag => triggerFilters.flatMap fun pr =>
    [pipeOf [cj "a"] [] push tag pr, pipeOf [{ cj "a" with manual := true }] [] push tag pr]

def needOn (id : String) (art : Bool) : List Core.Need := [⟨id, art⟩]

def twoJobPipes : List Core.Pipe :=
  (smallVariants "a").flatMap fun a => (smallVariants "b").flatMap fun b =>
    [[], needOn "a" false, needOn "a" true].map fun ns => pushAll [a, { b with needs := ns }] []

def mainLayouts : List (List Core.Job) :=
  [[], [cj "a"], [cj "a", cj "b"], [cj "a", { cj "b" with needs := needOn "a" false }]]

def finLayouts : List (List Core.Job) :=
  let f := cj "f"
  [[f], [{ f with onFailure := true }], [{ f with needs := needOn "a" false }],
   [{ f with needs := needOn "a" true }],
   [{ f with needs := [⟨"a", false⟩, ⟨"b", true⟩], onFailure := true }],
   [{ f with name := "Fin", allowFailure := true, steps := twoSteps, retry := 1, timeout := 7 }],
   [f, cj "g"], [{ f with onFailure := true }, { cj "g" with needs := needOn "a" false }]]

def finPipes : List Core.Pipe :=
  mainLayouts.flatMap fun ms => finLayouts.map fun fs => pushAll ms fs

def namePipes : List Core.Pipe :=
  [{ pushAll [sinkJob "a"] [] with name := "Pipe" }, { pushAll [cj "a"] [] with id := "gitlab-ci" },
   { pushAll [cj "a"] [{ cj "f" with name := "Fin" }] with id := "x_y", name := "P", tag := some ["v1"] }]

def allPipes : List Core.Pipe :=
  triggerPipes ++ (jobVariants "a").map (fun a => pushAll [a] []) ++ twoJobPipes ++ finPipes ++ namePipes

def ghaPipes : List Core.Pipe := allPipes.filter fun p => decide (InGha p)
def gitlabPipes : List Core.Pipe := allPipes.filter fun p => decide (InGitlab p)

def ghaAdapterCases : List (Json × Json) :=
  ghaPipes.map fun p =>
    (coreJson p, Json.mkObj [("gha", ghaJson (Gha.toGha p)), ("core", coreJson (Gha.fromGha (Gha.toGha p)))])

def gitlabAdapterCases : List (Json × Json) :=
  gitlabPipes.map fun p =>
    (coreJson p, Json.mkObj [("gitlab", gitlabJson (Gitlab.toGitlab p)),
      ("core", coreJson (Gitlab.fromGitlab (Gitlab.toGitlab p) (metaOf p)))])

def crossAdapterCases : List (Json × Json) :=
  ghaPipes.map fun p =>
    let viaGitlab := Gha.fromGha (Gha.toGha (Gitlab.fromGitlab (Gitlab.toGitlab p) (metaOf p)))
    let viaGha := Gitlab.fromGitlab (Gitlab.toGitlab (Gha.fromGha (Gha.toGha p) (metaOf p))) (metaOf p)
    (coreJson p, Json.mkObj [("cross", coreJson viaGitlab), ("rev", coreJson viaGha)])

end Adapters

end GenVectors

def main (args : List String) : IO UInt32 := do
  let (dir, only) ← match args with
    | d :: rest => pure (d, rest)
    | _ => do IO.eprintln "usage: gen-vectors <output-dir> [name...]"; return 2
  IO.FS.createDirAll dir
  let want (name : String) : Bool := only.isEmpty || only.contains name
  let files : List (String × String × List (Json × Json)) :=
    [("dag", "Pipemaster.Dag.layers_respect_needs", GenVectors.dagCases),
     ("matrix", "Pipemaster.Matrix.expand_mem", GenVectors.matrixCases),
     ("ledger", "Pipemaster.Ledger.complete", GenVectors.ledgerCases),
     ("gha", "Pipemaster.Gha.gha_from_to", GenVectors.ghaAdapterCases),
     ("gitlab", "Pipemaster.Gitlab.gitlab_from_to", GenVectors.gitlabAdapterCases),
     ("cross", "Pipemaster.RoundTrip.cross_roundtrip", GenVectors.crossAdapterCases)]
  for (name, thm, cases) in files do
    if want name then
      IO.FS.writeFile s!"{dir}/{name}.json" (GenVectors.emit thm cases)
      IO.println s!"{name}: {cases.length} cases"
  if want "expr" then
    IO.FS.writeFile s!"{dir}/expr.json"
      (GenVectors.emit "Pipemaster.Translate.tr_sound" GenVectors.exprCases (some GenVectors.exprEnvsJson))
    IO.println s!"expr: {GenVectors.exprCases.length} cases"
  return 0
