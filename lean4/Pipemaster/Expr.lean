import Mathlib.Data.String.Basic
import Mathlib.Data.List.Basic

namespace Pipemaster
namespace Expr

/-! Model of expression evaluation for the GHA and GitLab dialects, restricted to what the translator
touches. Strings are lowered with the ASCII-only `Char.toLower`: the model omits Unicode case folding,
number coercion between strings and numbers (the translator excludes the literals where it matters),
arrays, objects and every function but startsWith / endsWith / contains. Constructors outside that
fragment evaluate to null and are never translatable. -/

/-- ref_type of the running ref (canonical ref_type). -/
inductive RT | branch | tag
  deriving DecidableEq, Repr

/-- The four canonical event values with a GitLab counterpart (canonical event). -/
inductive Ev | push | pullRequest | schedule | workflowDispatch
  deriving DecidableEq, Repr

/-- Canonical references the model knows; `ghaNative` and `gitlabNative` are provider-native refs. -/
inductive Ref | refName | refType | event | sha | branch | tag | ghaNative | gitlabNative
  deriving DecidableEq, Repr

/-- The only regex shapes the translator emits or recognises, all case-insensitive (`/.../i`). -/
inductive Mode | exact | prefix | suffix | sub
  deriving DecidableEq, Repr

inductive Val | null | bool (b : Bool) | str (s : String)
  deriving DecidableEq, Repr

/-- An expression of either dialect. `rx x m s` is `x =~ /<m s>/i`, `nrx` is `!~`. -/
inductive E
  | null
  | bool (b : Bool)
  | str (s : String)
  | num (n : Int)
  | ref (r : Ref)
  | not (x : E)
  | and (l r : E)
  | or (l r : E)
  | eq (l r : E)
  | ne (l r : E)
  | lt (l r : E)
  | startsWith (a b : E)
  | endsWith (a b : E)
  | contains (a b : E)
  | call (name : String) (arg : E)
  | rx (x : E) (m : Mode) (s : String)
  | nrx (x : E) (m : Mode) (s : String)
  | rawRx (x : E) (re : String)
  | opq
  deriving DecidableEq, Repr

/-- The canonical environment: what the running pipeline is, independent of any dialect. -/
structure Env where
  refName : Option String
  refType : Option RT
  event : Option Ev
  sha : Option String
  nat : Option String

def rtName : RT → String
  | .branch => "branch"
  | .tag => "tag"

/-- Event names as GitHub reports them. -/
def evGha : Ev → String
  | .push => "push"
  | .pullRequest => "pull_request"
  | .schedule => "schedule"
  | .workflowDispatch => "workflow_dispatch"

/-- Mirrors gitlab.Events (expr/gitlab/vocab.go): the matching CI_PIPELINE_SOURCE values. -/
def evGl : Ev → String
  | .push => "push"
  | .pullRequest => "merge_request_event"
  | .schedule => "schedule"
  | .workflowDispatch => "web"

abbrev View := Ref → Option String

/-- Mirrors gha.Vocab (expr/gha/vocab.go): what a GHA expression sees. -/
def ghaView (env : Env) : View
  | .refName => env.refName
  | .refType => env.refType.map rtName
  | .event => env.event.map evGha
  | .sha => env.sha
  | .ghaNative => env.nat
  | _ => none

/-- Mirrors gitlab.Vocab (expr/gitlab/vocab.go): what a GitLab expression sees. CI_COMMIT_BRANCH
and CI_COMMIT_TAG exist only for a ref of that type. -/
def mapEnv (env : Env) : View
  | .refName => env.refName
  | .event => env.event.map evGl
  | .sha => env.sha
  | .branch => if env.refType = some .branch then env.refName else none
  | .tag => if env.refType = some .tag then env.refName else none
  | .gitlabNative => env.nat
  | _ => none

inductive Sem | gha | gitlab
  deriving DecidableEq, Repr

/-- ASCII-only lowering. -/
def lower (s : String) : String := String.ofList (s.toList.map Char.toLower)

/-- Mirrors expr.Semantics (expr/eval.go): null, false and "" are falsy; GitLab's && / || operand
test is Ruby truthiness instead (below). -/
def truthy : Val → Bool
  | .null => false
  | .bool b => b
  | .str s => s != ""

def rubyTruthy : Val → Bool
  | .null => false
  | .bool b => b
  | .str _ => true

def logicalTruthy : Sem → Val → Bool
  | .gha, v => truthy v
  | .gitlab, v => rubyTruthy v

/-- Mirrors gha.Semantics (expr/gha/vocab.go) and gitlab.Semantics (expr/gitlab/vocab.go) on the values
the model has. A string against null compares as "" only, the coercion the translator's literal
restrictions make exact. -/
def eqV : Sem → Val → Val → Bool
  | .gha, .str x, .str y => lower x == lower y
  | .gha, .null, .null => true
  | .gha, .null, .str y => y == ""
  | .gha, .str x, .null => x == ""
  | .gha, .bool a, .bool b => a == b
  | .gitlab, .str x, .str y => x == y
  | .gitlab, .null, .null => true
  | .gitlab, .bool a, .bool b => a == b
  | _, _, _ => false

/-- b occurs somewhere in a. -/
def isInfix (b a : List Char) : Bool := (List.range (a.length + 1)).any fun i => b.isPrefixOf (a.drop i)

def modeTest : Mode → String → String → Bool
  | .exact, a, b => a == b
  | .prefix, a, b => b.toList.isPrefixOf a.toList
  | .suffix, a, b => b.toList.isSuffixOf a.toList
  | .sub, a, b => isInfix b.toList a.toList

/-- A case-insensitive regex of shape `m s` against a value; null never matches. -/
def matchV (m : Mode) (s : String) : Val → Bool
  | .str x => modeTest m (lower x) (lower s)
  | _ => false

/-- startsWith / endsWith / contains on strings, case-insensitive; anything else is false. -/
def affixV (m : Mode) : Val → Val → Bool
  | .str x, .str y => modeTest m (lower x) (lower y)
  | _, _ => false

def refV (v : View) (r : Ref) : Val :=
  match v r with
  | some s => .str s
  | none => .null

/-- Mirrors expr.Eval (expr/eval.go). -/
def eval (sem : Sem) (v : View) : E → Val
  | .null => .null
  | .bool b => .bool b
  | .str s => .str s
  | .num _ => .null
  | .ref r => refV v r
  | .not x => .bool (!truthy (eval sem v x))
  | .and l r =>
    if logicalTruthy sem (eval sem v l) then eval sem v r else eval sem v l
  | .or l r =>
    if logicalTruthy sem (eval sem v l) then eval sem v l else eval sem v r
  | .eq l r => .bool (eqV sem (eval sem v l) (eval sem v r))
  | .ne l r => .bool (!eqV sem (eval sem v l) (eval sem v r))
  | .lt _ _ => .null
  | .startsWith a b => if sem = .gha then .bool (affixV .prefix (eval sem v a) (eval sem v b)) else .null
  | .endsWith a b => if sem = .gha then .bool (affixV .suffix (eval sem v a) (eval sem v b)) else .null
  | .contains a b => if sem = .gha then .bool (affixV .sub (eval sem v a) (eval sem v b)) else .null
  | .call _ _ => .null
  | .rx x m s => if sem = .gitlab then .bool (matchV m s (eval sem v x)) else .null
  | .nrx x m s => if sem = .gitlab then .bool (!matchV m s (eval sem v x)) else .null
  | .rawRx _ _ => .null
  | .opq => .null

/-- Truthiness of the result: what an `if` condition does with it. -/
def holds (sem : Sem) (v : View) (e : E) : Bool := truthy (eval sem v e)

end Expr
end Pipemaster
