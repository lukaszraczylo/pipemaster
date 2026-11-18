#!/usr/bin/env bash
# Fails when any Pipemaster declaration depends on an axiom outside the allowed set,
# including sorryAx, or when the Lean sources declare an axiom.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root/lean4"

lake build Pipemaster >&2

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/Axioms.lean" <<'LEAN'
import Pipemaster
import Lean
open Lean Elab Command

#eval show CommandElabM Unit from do
  let env ← getEnv
  let allowed : List Name := [``propext, ``Classical.choice, ``Quot.sound]
  let mut checked : Nat := 0
  let mut bad : Array String := #[]
  for (name, ci) in env.constants.map₁.toList do
    unless (`Pipemaster).isPrefixOf name do continue
    if let .axiomInfo _ := ci then
      bad := bad.push s!"{name}: declared axiom"
      continue
    let isThm := match ci with | .thmInfo _ => true | _ => false
    let isDef := match ci with | .defnInfo _ => true | _ => false
    unless isThm || isDef do continue
    let axs ← liftCoreM (collectAxioms name)
    checked := checked + 1
    let extra := axs.filter (fun a => !allowed.contains a)
    if !extra.isEmpty then
      bad := bad.push s!"{name}: {extra.toList}"
  if bad.isEmpty then
    logInfo m!"PASS: {checked} declarations use only propext, Classical.choice, Quot.sound"
  else
    throwError "FAIL:\n{"\n".intercalate bad.toList}"
LEAN

out="$(lake env lean "$tmp/Axioms.lean" 2>&1)" || { echo "$out"; exit 1; }
echo "$out" | grep -v "module system" | grep -q "PASS" || { echo "$out"; exit 1; }
echo "$out" | grep "PASS"
