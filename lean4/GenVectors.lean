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

/-- Environments for the trigger-fact vectors. A pull request has a branch ref type and a ref named
`<number>/merge` (PrFact); the first six are also free of tag runs (BranchFact), the last two are tag runs. -/
def factEnvs : List Expr.Env :=
  [⟨some "main", some .branch, some .push, some "abc", some "x"⟩,
   ⟨some "MAIN", some .branch, some .push, some "ABC", none⟩,
   ⟨some "dev", some .branch, some .schedule, none, none⟩,
   ⟨some "", some .branch, some .workflowDispatch, some "", some ""⟩,
   ⟨some "12/merge", some .branch, some .pullRequest, some "abc", none⟩,
   ⟨none, some .branch, some .pullRequest, none, none⟩,
   ⟨some "main", some .tag, some .push, some "abc", none⟩,
   ⟨some "v1", some .tag, some .workflowDispatch, none, none⟩]

def factEnvsJson : Json := Json.arr (factEnvs.map envJson).toArray

/-- The target branch a Woodpecker pull request run reports. -/
def wpTarget : Option String := some "main"

open Expr TranslateFacts in
/-- Mirrors PrFact and BranchFact on the environment list. -/
def factOk (fa : Bool) (env : Env) : Bool :=
  let pr := env.event == some .pullRequest
  (!pr || (env.refType == some .branch && (match env.refName with | some r => Translate.numLike r | none => true))) &&
  (!fa || pr || env.refType == some .branch)

def optBoolsJson (bs : List (Option Bool)) : Json :=
  Json.arr (bs.map fun | some b => Json.bool b | none => Json.null).toArray

open Expr Translate TranslateFacts in
def factCase (dir : String) (fa : Bool) (e : E) : Json × Json :=
  let on (f : Env → Bool) : List (Option Bool) := factEnvs.map fun env => if factOk fa env then some (f env) else none
  let out := match tr (assume fa e) with
    | none => Json.mkObj [("tr", Json.null)]
    | some t0 =>
      let t := fam t0
      Json.mkObj [("tr", exprJson t), ("tr_plain", exprJson t0),
        ("wp_ok", Json.bool (guardOk e && NoBareEv e)),
        ("src", optBoolsJson (on fun env => holds .gha (ghaView env) e)),
        ("tgt", optBoolsJson (on fun env => holds .gitlab (mapEnv env) t0)),
        ("wp", optBoolsJson (on fun env => holds .gitlab (wpMapS env wpTarget "") t)),
        ("wp_closed", optBoolsJson (on fun env => holds .gitlab (wpMapS env wpTarget "_closed") t)),
        ("wp_metadata", optBoolsJson (on fun env => holds .gitlab (wpMapS env wpTarget "_metadata") t))]
  (Json.mkObj [("dir", dir), ("expr", exprJson e)], out)

open Expr in
def factExprs : List E :=
  let rn := E.ref .refName
  let rt := E.ref .refType
  let ev := E.ref .event
  let guard := E.eq rt (E.str "branch")
  let names := ["main", "MAIN", "", "0", "-1", "12/merge", "release/1", "a\\"]
  let atoms := names.flatMap fun s =>
    [E.eq rn (E.str s), E.eq (E.str s) rn, E.ne rn (E.str s), E.ne (E.str s) rn]
  let others := [E.eq ev (E.str "pull_request"), E.eq ev (E.str "push"), guard, E.eq rt (E.str "tag"),
    E.ref .sha, E.startsWith rn (E.str "rel"), E.eq rn (E.ref .sha), E.eq rn E.null, E.opq]
  let guarded := [E.and guard (E.eq rn (E.str "main")), E.and guard (E.startsWith rn (E.str "rel")),
    E.and guard (E.ne rn (E.str "main")), E.and (E.eq rt (E.str "tag")) (E.eq rn (E.str "main"))]
  let core := [E.eq rn (E.str "main"), E.ne rn (E.str "main"), E.eq (E.str "MAIN") rn, E.eq rn (E.str "0"),
    E.eq ev (E.str "pull_request"), E.ne ev (E.str "pull_request"), guard, E.eq rt (E.str "tag"),
    E.and guard (E.eq rn (E.str "main")), E.startsWith rn (E.str "rel"), E.ref .sha]
  let p1 := atoms ++ others ++ guarded
  p1 ++ p1.map E.not ++ joinAll core core

def factCases : List (Json × Json) :=
  factExprs.map (factCase "gha-gitlab-facts" true) ++ factExprs.map (factCase "gha-gitlab-guard" false)


/-- GitLab runs for the merge request vectors: a merge request pipeline has a branch ref type and no CI_COMMIT_BRANCH
(glEnv), so `wpTarget` is what Woodpecker reports there. -/
def glEnvs : List Expr.Env :=
  [⟨some "main", some .branch, some .push, some "abc", none⟩,
   ⟨some "dev", some .branch, some .push, some "abc", none⟩,
   ⟨some "feature", some .branch, some .pullRequest, some "abc", none⟩,
   ⟨some "main", some .branch, some .pullRequest, some "abc", none⟩,
   ⟨some "v1", some .tag, some .push, some "abc", none⟩,
   ⟨some "main", some .branch, some .schedule, some "abc", none⟩,
   ⟨some "release/1", some .branch, some .workflowDispatch, some "abc", none⟩,
   ⟨none, none, none, none, none⟩]

def glEnvsJson : Json := Json.arr (glEnvs.map envJson).toArray

open Expr TranslateFacts in
def glPrCase (e : E) : Json × Json :=
  let src := glEnvs.map fun env => holds .gitlab (glEnv env) e
  let out := match glPr e with
    | none => Json.mkObj [("tr", Json.null)]
    | some t0 =>
      let t := famX false t0
      let wpAt (sfx : String) := Json.arr (glEnvs.map (fun env => Json.bool (holds .gitlab (Woodpecker.wpView (wpMapS env wpTarget sfx)) t))).toArray
      Json.mkObj [("tr", exprJson t), ("tr_plain", exprJson t0), ("src", Json.arr (src.map Json.bool).toArray),
        ("wp", wpAt ""), ("wp_closed", wpAt "_closed"), ("wp_metadata", wpAt "_metadata"),
        ("plain", Json.arr (glEnvs.map (fun env => Json.bool (holds .gitlab (Woodpecker.wpView (wpMap env wpTarget)) e))).toArray)]
  (Json.mkObj [("dir", "gitlab-woodpecker-pr"), ("expr", exprJson e)], out)

open Expr in
def glPrExprs : List E :=
  let br := E.ref .branch
  let ev := E.ref .event
  let rxs := [Mode.exact, .prefix, .suffix, .sub].flatMap fun m =>
    ["main", "rel", "1"].flatMap fun s => [E.rx br m s, E.nrx br m s]
  let eqs := ["main", "", "Main", "release/1"].flatMap fun s => [E.eq br (E.str s), E.ne br (E.str s)]
  let shapes := [E.eq (E.str "main") br, E.ref .branch, E.rx (E.str "x") .exact "main", E.rx br .exact "", E.opq,
    E.lt br (E.str "m"), E.startsWith br (E.str "m")]
  let others := [E.rx (E.ref .tag) .exact "v1", E.nrx (E.ref .tag) .exact "v1", E.rx ev .exact "merge_request_event",
    E.nrx ev .exact "merge_request_event", E.eq ev (E.str "push"), E.ne ev (E.str "push")]
  let p1 := rxs ++ eqs ++ shapes ++ others
  let core := [E.rx br .exact "main", E.nrx br .exact "main", E.eq br (E.str "main"), E.ne br (E.str "main"),
    E.rx br .prefix "rel", E.rx (E.ref .tag) .exact "v1", E.rx ev .exact "merge_request_event"]
  p1 ++ p1.map E.not ++ (p1.map E.not).map E.not ++ joinAll core core

open Expr in
def glPrCases : List (Json × Json) :=
  (glPrExprs.filter fun e => Woodpecker.GlSub e || true).map glPrCase

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
  Json.mkObj [("name", g.name), ("stages", strsJson g.stages),
    ("rules", match g.rules with | some rs => Json.arr (rs.map trig).toArray | none => Json.null),
    ("jobs", Json.arr (g.jobs.map glJobJson).toArray), ("marks", Json.arr (g.marks.map trig).toArray)]

open Pipemaster.Woodpecker in
def wpEntryJson (e : Woodpecker.Entry) : Json :=
  Json.mkObj [("events", strsJson e.events), ("branch", strsJson e.branch), ("ref", strsJson e.ref),
    ("status", strsJson e.status)]

open Pipemaster.Woodpecker in
def wpStepJson (s : Woodpecker.WStep) : Json :=
  Json.mkObj [("name", s.name), ("cmds", strsJson s.cmds), ("env", varsJson s.env), ("ignore", s.ignore),
    ("shadow", strsJson s.shadow)]

open Pipemaster.Woodpecker in
def wpTrigJson (t : Woodpecker.Trig) : Json := Json.mkObj [("kind", t.kind), ("include", strsJson t.incl)]

open Pipemaster.Woodpecker in
def wpFileJson (f : Woodpecker.WFile) : Json :=
  Json.mkObj [("name", f.name), ("whens", Json.arr (f.whens.map wpEntryJson).toArray),
    ("deps", Json.arr (f.deps.map fun d => Json.mkObj [("name", d.name), ("optional", d.optional)]).toArray),
    ("steps", Json.arr (f.steps.map wpStepJson).toArray), ("noop", f.noop), ("job_env", varsJson f.jobEnv),
    ("allow_failure", f.allowFailure),
    ("manual", match f.manual with | some ts => Json.arr (ts.map wpTrigJson).toArray | none => Json.null),
    ("finally", match f.fin with
      | some (ns, noIf) => Json.mkObj [("needs", Json.arr (ns.map needJ).toArray), ("no_if", noIf)]
      | none => Json.null),
    ("tag_idx", optNat f.tagIdx), ("platform", match f.platform with | some x => Json.str x | none => Json.null),
    ("tags", strsJson f.tags)]

open Pipemaster.Drone in
def droneStepJson (s : Drone.DStep) : Json :=
  Json.mkObj [("name", s.name), ("cmds", strsJson s.cmds), ("env", varsJson s.env), ("ignore", s.ignore)]

open Pipemaster.Drone in
def droneDocJson (d : Drone.DDoc) : Json :=
  Json.mkObj [("name", d.name),
    ("trigger", match d.trigger with
      | some t => Json.mkObj [("events", strsJson t.events), ("branch", strsJson t.branch), ("ref", strsJson t.ref),
          ("status", strsJson t.status)]
      | none => Json.null),
    ("deps", strsJson d.deps), ("steps", Json.arr (d.steps.map droneStepJson).toArray), ("env", varsJson d.env),
    ("platform", match d.platform with | some x => Json.str x | none => Json.null), ("tags", strsJson d.tags),
    ("noop", d.noop), ("allow_failure", d.allowFailure),
    ("manual", match d.manual with | some ts => Json.arr (ts.map wpTrigJson).toArray | none => Json.null),
    ("finally", match d.fin with
      | some (ns, noIf) => Json.mkObj [("needs", Json.arr (ns.map needJ).toArray), ("no_if", noIf)]
      | none => Json.null),
    ("if_trigs", match d.ifTrigs with | some ts => Json.arr (ts.map wpTrigJson).toArray | none => Json.null)]

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

/-- The pipelines both providers accept, so the GHA, GitLab and cross vectors share one set. -/
def ghaPipes : List Core.Pipe := allPipes.filter fun p => decide (RoundTrip.Both p)
def gitlabPipes : List Core.Pipe := ghaPipes

def ghaAdapterCases : List (Json × Json) :=
  ghaPipes.map fun p =>
    (coreJson p, Json.mkObj [("gha", ghaJson (Gha.toGha p)), ("core", coreJson (Gha.fromGha (Gha.toGha p)))])

def gitlabAdapterCases : List (Json × Json) :=
  gitlabPipes.map fun p =>
    (coreJson p, Json.mkObj [("gitlab", gitlabJson (Gitlab.toGitlab p)),
      ("core", coreJson (Gitlab.fromGitlab (Gitlab.toGitlab p) (Gitlab.gitlabMeta p)))])

/-- Pipelines that exercise the Woodpecker-only markers: a job with no step, a step variable that repeats a job
variable, `$` in commands and values, a tag filter next to other triggers, and a finally job with needs. -/
def wpExtraPipes : List Core.Pipe :=
  let noSteps : Core.Job := { cj "a" with steps := [] }
  let shadow : Core.Job :=
    { cj "a" with env := [⟨"K", "v"⟩, ⟨"M", "w"⟩],
                  steps := [{ cstep "s1" ["echo 1"] with env := [⟨"K", "v"⟩, ⟨"N", "x"⟩] },
                            { cstep "s2" ["echo 2"] with env := [⟨"K", "other"⟩] }] }
  let dollar : Core.Job :=
    { cj "a" with env := [⟨"P", "a$b"⟩, ⟨"Q", "$$"⟩],
                  steps := [{ cstep "s1" ["echo $HOME", "echo ${X}", "echo $$"] with env := [⟨"R", "$y"⟩] }] }
  [pushAll [noSteps] [], pushAll [noSteps, { cj "b" with steps := [] }] [{ cj "f" with steps := [] }],
   pushAll [shadow] [], pushAll [dollar] [],
   pipeOf [cj "a", { cj "b" with manual := true }] [] (some ["main"]) (some ["v1", "v2"]) (some ["dev"]),
   pipeOf [cj "a"] [{ cj "f" with needs := needOn "a" true }] none (some ["v1"]) none,
   pipeOf [{ cj "a" with manual := true }] [{ cj "f" with onFailure := true, steps := [] }] (some ["x"]) (some []) none] ++
  [[], ["linux/amd64"], ["linux/amd64", "x"], ["linux/arm64/v8", "x"], ["darwin/arm64", "linux/amd64", "x"],
   ["Linux/amd64"], ["linux/amd64/v"], ["a/b/c"], ["linux"]].flatMap fun r =>
    [pushAll [{ cj "a" with runner := r }] [], pushAll [cj "a"] [{ cj "f" with runner := r }]]

def wpPipes : List Core.Pipe :=
  (allPipes ++ wpExtraPipes).filter fun p => decide (Wf p ∧ Woodpecker.WpOk p)

def woodpeckerAdapterCases : List (Json × Json) :=
  wpPipes.map fun p =>
    (coreJson p, Json.mkObj [("woodpecker", Json.arr ((Woodpecker.toWp p).map wpFileJson).toArray),
      ("raw", coreJson (Woodpecker.rawWp (Woodpecker.toWp p))),
      ("core", coreJson (Woodpecker.fromWp (Woodpecker.toWp p) (Woodpecker.wpMeta p)))])

def dronePipes : List Core.Pipe :=
  (allPipes ++ wpExtraPipes).filter fun p => decide (Wf p ∧ Drone.DroneOk p)

def droneAdapterCases : List (Json × Json) :=
  dronePipes.map fun p =>
    (coreJson p, Json.mkObj [("drone", Json.arr ((Drone.toDrone p).map droneDocJson).toArray),
      ("raw", coreJson (Drone.rawDrone (Drone.toDrone p))),
      ("core", coreJson (Drone.fromDrone (Drone.toDrone p) (Drone.droneMeta p)))])

open Pipemaster.Tekton in
def tekStepJson (s : Tekton.TStep) : Json :=
  Json.mkObj [("name", s.name), ("disp", s.disp), ("script", strsJson s.script), ("env", varsJson s.env)]

open Pipemaster.Tekton in
def tekTaskJson (t : Tekton.TTask) : Json :=
  Json.mkObj [("name", t.name), ("env", varsJson t.env), ("steps", Json.arr (t.steps.map tekStepJson).toArray),
    ("noop", t.noop),
    ("needs", match t.needs with | some ns => Json.arr (ns.map needJ).toArray | none => Json.null)]

open Pipemaster.Tekton in
def tekPTaskJson (t : Tekton.PTask) : Json :=
  Json.mkObj [("name", t.name), ("disp", t.disp), ("task", tekTaskJson t.task), ("run_after", strsJson t.runAfter),
    ("retries", optNat t.retries), ("timeout", Lean.toJson t.timeout), ("on_error", t.onError),
    ("failed_only", t.failedOnly)]

open Pipemaster.Tekton in
def tekDocJson (d : Tekton.TDoc) : Json :=
  Json.mkObj [("name", d.name), ("disp", d.disp), ("tasks", Json.arr (d.tasks.map tekPTaskJson).toArray),
    ("finally", Json.arr (d.fin.map tekPTaskJson).toArray), ("trigs", Json.arr (d.trigs.map wpTrigJson).toArray)]

/-- Jobs with several steps, and a real step named like the placeholder (`noop` running `true`), alone, beside a
job with no step, and in a finally job. The parser must keep the real step and drop the placeholder. -/
def tekStepPipes : List Core.Pipe :=
  let real : Core.Step := cstep "noop" ["true"]
  let three : List Core.Step :=
    [cstep "s1" ["echo 1"], { cstep "s2" ["echo 2", "echo 3"] with name := "Two" }, cstep "s3" ["echo 4"]]
  [pushAll [{ cj "a" with steps := three }] [],
   pushAll [{ cj "a" with steps := three, env := [⟨"K", "v"⟩], retry := 2 }] [],
   pushAll [cj "a"] [{ cj "f" with steps := three }],
   pushAll [{ cj "a" with steps := [real] }] [],
   pushAll [{ cj "a" with steps := [real] }, { cj "b" with steps := [] }] [],
   pushAll [{ cj "a" with steps := [] }, { cj "b" with steps := [real] }] [],
   pushAll [{ cj "a" with steps := [cstep "s1" ["echo 1"], real] }] [],
   pushAll [{ cj "a" with steps := [real, cstep "s1" ["echo 1"]] }] [],
   pushAll [cj "a"] [{ cj "f" with steps := [real] }],
   pushAll [{ cj "a" with steps := [] }] [{ cj "f" with steps := [real], onFailure := true }]]

/-- Pipelines with retry runs, which GitLab and Drone also take, and jobs with no step, which Tekton fills with a
placeholder step. -/
def tekExtraPipes : List Core.Pipe :=
  [2, 3, 4].flatMap (fun r => finLayouts.map fun fs => pushAll [{ cj "a" with retry := r }] fs) ++
    [pushAll [{ cj "a" with steps := [] }] [], pushAll [cj "a"] [{ cj "f" with steps := [] }],
     pushAll [{ cj "a" with steps := [], retry := 2, timeout := 3 }] [{ cj "f" with steps := [], onFailure := true }],
     { pushAll [{ cj "a" with retry := 3 }] [] with id := "my-ci", name := "My CI", pr := some ["main"] },
     { pushAll [{ cj "a" with retry := 2, timeout := 90 }] [] with tag := some ["v*"], push := none },
     { pushAll [{ cj "a" with name := "Build", retry := 4 }, { cj "b" with needs := needOn "a" false }] [] with
         pr := some [] }] ++ tekStepPipes

def tektonPipes : List Core.Pipe :=
  (allPipes ++ wpExtraPipes ++ tekExtraPipes).filter fun p => decide (Wf p ∧ Tekton.TekOk p)

def tektonAdapterCases : List (Json × Json) :=
  tektonPipes.map fun p =>
    (coreJson p, Json.mkObj [("tekton", tekDocJson (Tekton.toTekton p)),
      ("raw", coreJson (Tekton.rawTekton (Tekton.toTekton p))),
      ("core", coreJson (Tekton.fromTekton (Tekton.toTekton p) (Tekton.tektonMeta p)))])

open Pipemaster.Argo in
def argoDepJson : Argo.DepExpr → Json
  | .atom t r => Json.mkObj [("atom", t), ("res", match r with | some x => Json.str x | none => Json.null)]
  | .and l r => Json.mkObj [("and", Json.arr #[argoDepJson l, argoDepJson r])]
  | .or l r => Json.mkObj [("or", Json.arr #[argoDepJson l, argoDepJson r])]
  | .not x => Json.mkObj [("not", argoDepJson x)]

open Pipemaster.Argo in
def argoWaitJson : Argo.Wait → Json
  | .deps l => Json.mkObj [("deps", strsJson l)]
  | .depends d => Json.mkObj [("depends", argoDepJson d)]

open Pipemaster.Argo in
def argoStepJson (s : Argo.AStep) : Json :=
  Json.mkObj [("name", s.name), ("disp", s.disp), ("script", strsJson s.script), ("env", varsJson s.env)]

open Pipemaster.Argo in
def argoTaskJson (t : Argo.ATask) : Json :=
  Json.mkObj [("name", t.name), ("disp", t.disp), ("wait", argoWaitJson t.wait), ("retries", optNat t.retries),
    ("timeout", Lean.toJson t.timeout), ("on_error", t.onError), ("failed_only", t.failedOnly),
    ("steps", Json.arr (t.steps.map argoStepJson).toArray), ("noop", t.noop), ("job_env", varsJson t.jobEnv)]

open Pipemaster.Argo in
def argoDocJson (d : Argo.ADoc) : Json :=
  Json.mkObj [("name", d.name), ("disp", d.disp), ("tasks", Json.arr (d.tasks.map argoTaskJson).toArray),
    ("finally", Json.arr (d.fin.map argoTaskJson).toArray), ("trigs", Json.arr (d.trigs.map wpTrigJson).toArray)]

/-- Pipelines that exercise what Argo adds to the Tekton set: job variables beside step variables, a finally job with
needs (which the raw parse loses and the metadata restores), with and without needs on a main job, and a job named
`main`-like or reserved-adjacent. -/
def argoExtraPipes : List Core.Pipe :=
  let two : List Core.Step := [cstep "s1" ["echo 1"], cstep "s2" ["echo 2"]]
  [pushAll [{ cj "a" with env := [⟨"K", "v"⟩], steps := two }] [],
   pushAll [{ cj "a" with env := [⟨"K", "v"⟩, ⟨"L", "w"⟩], steps := [{ cstep "s1" ["echo 1"] with env := [⟨"M", "x"⟩] }] }] [],
   pushAll [{ cj "a" with env := [⟨"K", "v"⟩], steps := [] }] [],
   pushAll [cj "a"] [{ cj "f" with needs := needOn "a" false }],
   pushAll [cj "a"] [{ cj "f" with needs := needOn "a" false, onFailure := true }],
   pushAll [cj "a", { cj "b" with needs := needOn "a" false }] [{ cj "f" with needs := needOn "a" false }],
   pushAll [cj "a", { cj "b" with needs := needOn "a" false }] [{ cj "f" with needs := needOn "b" false, onFailure := true }],
   pushAll [{ cj "a" with retry := 2, timeout := 90, allowFailure := true }] [],
   pushAll [{ cj "a" with retry := 1 }] [],
   pushAll [{ cj "a" with retry := 1, timeout := 5, steps := [] }] [{ cj "f" with retry := 1, onFailure := true }],
   pushAll [{ cj "a" with name := "Build" }] [{ cj "f" with name := "Notify", onFailure := true }]] ++ tekExtraPipes

def argoPipes : List Core.Pipe :=
  (allPipes ++ wpExtraPipes ++ argoExtraPipes).filter fun p => decide (Wf p ∧ Argo.ArgoOk p)

def argoAdapterCases : List (Json × Json) :=
  argoPipes.map fun p =>
    (coreJson p, Json.mkObj [("argo", argoDocJson (Argo.toArgo p)),
      ("raw", coreJson (Argo.rawArgo (Argo.toArgo p))),
      ("core", coreJson (Argo.fromArgo (Argo.toArgo p) (Argo.argoMeta p)))])

open Pipemaster.Concourse in
def concTaskJson (t : Concourse.CTask) : Json :=
  Json.mkObj [("name", t.name), ("script", strsJson t.script), ("carried", strsJson t.carried), ("cid", t.cid), ("cname", t.cname),
    ("cenv", varsJson t.cenv)]

open Pipemaster.Concourse in
def concJobJson (j : Concourse.CJob) : Json :=
  Json.mkObj [("name", j.name), ("disp", j.disp), ("passed", strsJson j.passed),
    ("body", Json.arr (j.body.map concTaskJson).toArray), ("hook", Json.arr (j.hook.map concTaskJson).toArray),
    ("carry", coreJobJson j.carry)]

open Pipemaster.Concourse in
def concDocJson (d : Concourse.CDoc) : Json :=
  Json.mkObj [("file", d.file), ("carry", coreJson d.carry), ("jobs", Json.arr (d.jobs.map concJobJson).toArray),
    ("fin_job", d.finJob), ("fin_tasks", strsJson d.finTasks),
    ("fin_copies", Json.arr (d.finCopies.map fun c => Json.mkObj [("job", c.1), ("tasks", strsJson c.2)]).toArray)]

/-- Pipelines that exercise what Concourse adds to the Woodpecker set: a need on a job with no step (the producer, the
consumer and both), a three-job chain with a finally job on its sink and a failure copy on the other jobs, a fan-out and a fan-in with finally jobs (lost), finally steps whose IDs clash with the steps of
the sink job (the emitter appends `-2`, `-3`), a pipeline with two sinks (the finally job has no hook), job and step
names with digits, dashes and underscores, and script lines that read like markers. -/
def concExtraPipes : List Core.Pipe :=
  let two : List Core.Step := [cstep "s1" ["echo 1"], cstep "s2" ["echo 2"]]
  let clash : Core.Job := { cj "a" with steps := [cstep "s1" ["echo 1"], cstep "s1-2" ["echo 2"]] }
  let marker : Core.Job :=
    { cj "a" with steps := [cstep "s1" ["# pipemaster:ctx", "echo (x)", "  x  ", "exit 0 # pipemaster:guard"]] }
  [pushAll [{ cj "a" with steps := [] }, { cj "b" with needs := needOn "a" false }] [],
   pushAll [cj "a", { cj "b" with needs := needOn "a" false, steps := [] }] [],
   pushAll [{ cj "a" with steps := [] }, { cj "b" with needs := needOn "a" false, steps := [] }] [],
   pushAll [{ cj "a" with manual := true }, { cj "b" with needs := needOn "a" false }] [],
   pushAll [cj "a", { cj "b" with needs := needOn "a" false }, { cj "c" with needs := needOn "b" false }] [cj "f"],
   pushAll [cj "a", { cj "b" with needs := needOn "a" false }, { cj "c" with needs := [⟨"a", false⟩, ⟨"b", true⟩] }]
     [{ cj "f" with onFailure := true }],
   pushAll [cj "a", { cj "b" with needs := needOn "a" false }, { cj "c" with needs := needOn "a" false }] [cj "f"],
   pushAll [{ cj "a" with steps := two }, { cj "b" with needs := needOn "a" false }, { cj "c" with needs := needOn "b" false }]
     [cj "f", cj "g"],
   pushAll [clash] [cj "f"], pushAll [{ cj "a" with steps := two }] [cj "f", cj "g"],
   pushAll [{ cj "a" with steps := two }, cj "b"] [cj "f"],
   pushAll [cj "a", cj "b"] [{ cj "f" with steps := [] }],
   pushAll [{ cj "a" with steps := [] }] [{ cj "f" with steps := two }],
   pushAll [{ cj "build-1" with steps := [cstep "unit_test" ["make"], cstep "e2e-2" ["make e2e"]] },
     { cj "deploy_2" with needs := needOn "build-1" false }] [],
   pushAll [{ cj "a" with steps := [cstep "s1" ["echo two", ""]] }] [],
   pushAll [{ cj "a" with steps := [cstep "s1" ["echo a", "", ""], cstep "s2" ["echo a\necho b"]] }] [cj "f"],
   pushAll [cj "a"] [{ cj "f" with steps := [cstep "s1" ["echo f", ""]] }],
   pushAll [marker] [], pushAll [{ cj "a" with env := [⟨"K", "((v))"⟩, ⟨"L", "true"⟩, ⟨"M", ""⟩] }] []] ++ wpExtraPipes

def concourseOk (p : Core.Pipe) : Bool := decide (Wf p ∧ Concourse.ConcourseOk p)

def concoursePipes : List Core.Pipe :=
  (allPipes ++ concExtraPipes).filter concourseOk

def concourseAdapterCases : List (Json × Json) :=
  concoursePipes.map fun p =>
    (coreJson p, Json.mkObj [("concourse", concDocJson (Concourse.toConcourse p)),
      ("raw", coreJson (Concourse.rawConcourse (Concourse.toConcourse p))),
      ("core", coreJson (Concourse.fromConcourse (Concourse.toConcourse p) (Core.metaOf p)))])

def crossAdapterCases : List (Json × Json) :=
  ghaPipes.map fun p =>
    let viaGitlab := Gha.fromGha (Gha.toGha (Gitlab.fromGitlab (Gitlab.toGitlab p) (Gitlab.gitlabMeta p)))
    let viaGha := Gitlab.fromGitlab (Gitlab.toGitlab (Gha.fromGha (Gha.toGha p) (metaOf p))) (Gitlab.gitlabMeta p)
    (coreJson p, Json.mkObj [("cross", coreJson viaGitlab), ("rev", coreJson viaGha)])

/-! ## Chain vectors (Task 9)

Every ordered choice of three distinct providers over `chainAdapters` (336 over eight): the first is the origin, the other
two are the hops. Each case holds the IR as the origin parsed it, the IR after each hop's parse
with no metadata applied, and the IR after unwinding the stack. The pipelines are a reduced subset of `allPipes`
that lie in the fragment of every adapter (`Chain.chainFrag_adapters_iff`, with `Chain.chainFrag_run` for the hops):
- triggers: push and pr over {absent, no filter, [x, y]} and a lone tag trigger, with one plain job;
- one job: the 17 variants of `jobVariants`;
- two main jobs: 3 variants of the first and 2 of the second, with needs none, plain or artifacts;
- finally: 2 main layouts (a; a and b needing a) times the 8 finally layouts;
- pipeline name and ID: three cases;
- the Woodpecker extras (`wpExtraPipes`): no-step jobs, shadowed variables, `$` text, tag filters and runner labels, with
  and without an `os/arch` platform label. Runner lists are sorted, because `ir.Normalize` sorts the labels
  (ir/normalize.go:56) and the model keeps list order.
The full `allPipes` set gives a 10 MB file; this subset keeps the file near the size of the largest other one.

Bound: the number of ordered triples grows as n(n-1)(n-2), so at nine adapters (504 triples) the full subset
would give about 57 times 504 cases, near 57 MB. Each triple therefore takes at most `chainCaseCap / triples`
pipelines (at least one), picked by `chainPick` so that the triples together still cover every pipeline. A case is
about 2 KB (1990 bytes measured at four adapters), so the cap of 1400 cases keeps `chain.json` under 3 MB
(at most 2.8 MB; at nine adapters the bound gives 2 pipelines per triple, 1008 cases, about 2.0 MB). At eight
adapters (336 triples) the bound gives 4 pipelines per triple, 1344 cases, 2.5 MB. At seven adapters (210 triples)
the bound gives 6 pipelines per triple, 1260 cases. At four adapters the cap allows every
pipeline. -/

def chainCaseCap : Nat := 1400

def chainPipes : List Core.Pipe :=
  (triggerPipes.filter fun p => p.jobs.all (fun j => !j.manual) && (p.tag.isNone || (p.push.isNone && p.pr.isNone))) ++
  (jobVariants "a").map (fun a => pushAll [a] []) ++
  ((smallVariants "a").take 3).flatMap (fun a => ((smallVariants "b").take 2).flatMap fun b =>
    [[], needOn "a" false, needOn "a" true].map fun ns => pushAll [a, { b with needs := ns }] []) ++
  [[cj "a"], [cj "a", { cj "b" with needs := needOn "a" false }]].flatMap (fun ms => finLayouts.map fun fs => pushAll ms fs) ++
  namePipes ++ wpExtraPipes

/-- The pipelines in the fragment of every adapter (decided once per pipeline). -/
def chainFragPipes : List Core.Pipe :=
  chainPipes.filter fun p => decide (RoundTrip.Both p ∧ (Wf p ∧ Woodpecker.WpOk p) ∧ Drone.DroneOk p ∧ Tekton.TekOk p ∧
      Argo.ArgoOk p ∧ Argo.FinDrop p ∧ Concourse.ConcourseOk p)

def chainAdapters : List (String × Chain.Adapter) :=
  [("gha", Chain.gha), ("gitea", Chain.gitea), ("forgejo", Chain.forgejo), ("gitlab", Chain.gitlab),
   ("woodpecker", Chain.woodpecker), ("drone", Chain.drone), ("tekton", Chain.tekton),
   ("argo", Chain.argo), ("concourse", Chain.concourse)]

def chainTriples : List ((String × Chain.Adapter) × (String × Chain.Adapter) × (String × Chain.Adapter)) :=
  chainAdapters.flatMap fun a => chainAdapters.flatMap fun b => chainAdapters.flatMap fun c =>
    if a.1 ≠ b.1 ∧ a.1 ≠ c.1 ∧ b.1 ≠ c.1 then [(a, b, c)] else []

/-- The pipelines per triple under the bound. -/
def chainPerTriple : Nat :=
  min chainFragPipes.length (max 1 (chainCaseCap / chainTriples.length))

/-- The smallest stride above one that shares no factor with `n`, so that stepping by it visits every index. -/
def chainStride (n : Nat) : Nat := ((List.range 64).find? fun s => 2 ≤ s ∧ Nat.gcd s n = 1).getD 1

/-- The pipelines of triple number `t`: all of them when the bound allows, else `chainPerTriple` of them, taken
at consecutive positions of the stride walk so that distinct triples start at distinct positions. -/
def chainPick (t : Nat) : List Core.Pipe :=
  let n := chainFragPipes.length
  if chainPerTriple ≥ n then chainFragPipes
  else (List.range chainPerTriple).filterMap fun i =>
    chainFragPipes[((t * chainPerTriple + i) * chainStride n) % n]?

def chainCases : List (Json × Json) :=
  chainTriples.zipIdx.flatMap fun ((a, b, c), t) =>
    (chainPick t).map fun p =>
      let p1 := b.2.parse (b.2.emit p)
      let r := Chain.hops p [b.2, c.2]
      (Json.mkObj [("chain", strsJson [a.1, b.1, c.1]), ("core", coreJson p)],
        Json.mkObj [("stages", Json.arr #[coreJson p1, coreJson (c.2.parse (c.2.emit p1))]),
          ("final", coreJson (Chain.unwind r.1 r.2))])

end Adapters

end GenVectors

def main (args : List String) : IO UInt32 := do
  let (dir, only) ← match args with
    | d :: rest => pure (d, rest)
    | _ => do IO.eprintln "usage: gen-vectors <output-dir> [name...]"; return 2
  IO.FS.createDirAll dir
  let want (name : String) : Bool := only.isEmpty || only.contains name
  let files : List (String × String × (Unit → List (Json × Json))) :=
    [("dag", "Pipemaster.Dag.layers_respect_needs", fun _ => GenVectors.dagCases),
     ("matrix", "Pipemaster.Matrix.expand_mem", fun _ => GenVectors.matrixCases),
     ("ledger", "Pipemaster.Ledger.complete", fun _ => GenVectors.ledgerCases),
     ("gha", "Pipemaster.Gha.gha_from_to", fun _ => GenVectors.ghaAdapterCases),
     ("gitlab", "Pipemaster.Gitlab.gitlab_from_to", fun _ => GenVectors.gitlabAdapterCases),
     ("woodpecker", "Pipemaster.Woodpecker.woodpecker_from_to", fun _ => GenVectors.woodpeckerAdapterCases),
     ("drone", "Pipemaster.Drone.drone_from_to", fun _ => GenVectors.droneAdapterCases),
     ("tekton", "Pipemaster.Tekton.tekton_from_to", fun _ => GenVectors.tektonAdapterCases),
     ("argo", "Pipemaster.Argo.argo_from_to", fun _ => GenVectors.argoAdapterCases),
     ("concourse", "Pipemaster.Concourse.concourse_from_to", fun _ => GenVectors.concourseAdapterCases),
     ("cross", "Pipemaster.RoundTrip.cross_roundtrip", fun _ => GenVectors.crossAdapterCases),
     ("chain", "Pipemaster.Chain.chain_roundtrip", fun _ => GenVectors.chainCases)]
  for (name, thm, mk) in files do
    if want name then
      let cases := mk ()
      IO.FS.writeFile s!"{dir}/{name}.json" (GenVectors.emit thm cases)
      IO.println s!"{name}: {cases.length} cases"
  if want "expr" then
    IO.FS.writeFile s!"{dir}/expr.json"
      (GenVectors.emit "Pipemaster.Translate.tr_sound" GenVectors.exprCases (some GenVectors.exprEnvsJson))
    IO.println s!"expr: {GenVectors.exprCases.length} cases"
  if want "expr-facts" then
    IO.FS.writeFile s!"{dir}/expr-facts.json"
      (GenVectors.emit "Pipemaster.TranslateFacts.trFactsWp_sound" GenVectors.factCases (some GenVectors.factEnvsJson))
    IO.println s!"expr-facts: {GenVectors.factCases.length} cases"
  if want "expr-glpr" then
    IO.FS.writeFile s!"{dir}/expr-glpr.json"
      (GenVectors.emit "Pipemaster.TranslateFacts.glWp_sound" GenVectors.glPrCases (some GenVectors.glEnvsJson))
    IO.println s!"expr-glpr: {GenVectors.glPrCases.length} cases"
  return 0
