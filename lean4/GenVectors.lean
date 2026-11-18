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

def emit (theorem_ : String) (cases : List (Json × Json)) : String :=
  let lines := cases.map fun (i, o) => (Json.mkObj [("in", i), ("out", o)]).compress
  s!"\{\"theorem\":\"{theorem_}\",\"cases\":[\n" ++ ",\n".intercalate lines ++ "\n]}\n"

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

end GenVectors

def main (args : List String) : IO UInt32 := do
  let dir ← match args with
    | [d] => pure d
    | _ => do IO.eprintln "usage: gen-vectors <output-dir>"; return 2
  IO.FS.createDirAll dir
  let files : List (String × String × List (Json × Json)) :=
    [("dag", "Pipemaster.Dag.layers_respect_needs", GenVectors.dagCases),
     ("matrix", "Pipemaster.Matrix.expand_mem", GenVectors.matrixCases),
     ("ledger", "Pipemaster.Ledger.complete", GenVectors.ledgerCases)]
  for (name, thm, cases) in files do
    IO.FS.writeFile s!"{dir}/{name}.json" (GenVectors.emit thm cases)
    IO.println s!"{name}: {cases.length} cases"
  return 0
