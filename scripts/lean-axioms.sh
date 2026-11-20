#!/usr/bin/env bash
# Fails when any Pipemaster declaration depends on an axiom outside the allowed set,
# including sorryAx, or when the Lean sources declare an axiom.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root/lean4"

lake build Pipemaster >&2

# Theorems the gate must find; a rename or deletion fails the script.
required=(
  Pipemaster.Dag.layers_respect_needs
  Pipemaster.Dag.layers_none_iff
  Pipemaster.Dag.layers_length
  Pipemaster.Matrix.expand_card
  Pipemaster.Matrix.expand_mem
  Pipemaster.Matrix.expand_combo_origin
  Pipemaster.Matrix.expand_survivor_preserved
  Pipemaster.Matrix.expand_include_merged
  Pipemaster.Matrix.expand_include_appended
  Pipemaster.Matrix.expand_include_accounted
  Pipemaster.Ledger.complete
  Pipemaster.Ledger.emitted_report_disjoint
  Pipemaster.Ledger.report_unique
  Pipemaster.Ledger.report_level
  Pipemaster.Ledger.finish_unaccounted
  Pipemaster.Translate.tr_sound
  Pipemaster.Translate.tr_sound_rev
  Pipemaster.Translate.tr_none_iff
  Pipemaster.Translate.tr_none_iff_rev
  Pipemaster.Translate.naive_eq_unsound
  Pipemaster.Translate.naive_and_unsound
  Pipemaster.TranslateClass.eval_congr
  Pipemaster.TranslateClass.rename_sound
  Pipemaster.TranslateClass.tr_sound_dialect
  Pipemaster.TranslateClass.tr_sound_rev_dialect
  Pipemaster.TranslateClass.event_cmp
  Pipemaster.TranslateClass.event_eq_rename
  Pipemaster.TranslateClass.event_ne_rename
  Pipemaster.TranslateClass.event_rx_rename
  Pipemaster.Gha.gha_from_to
  Pipemaster.Gitlab.gitlab_from_to
  Pipemaster.Woodpecker.eq_empty_differs
  Pipemaster.Woodpecker.eq_null_differs
  Pipemaster.Woodpecker.not_gitlab_class
  Pipemaster.Woodpecker.eval_woodpecker
  Pipemaster.Woodpecker.woodpecker_sound
  Pipemaster.Woodpecker.woodpecker_event_rx
  Pipemaster.RoundTrip.cross_roundtrip
  Pipemaster.RoundTrip.cross_roundtrip_rev
  Pipemaster.RoundTrip.both_iff
  Pipemaster.Core.fromGitlab_restore
  Pipemaster.Chain.adapters_closed
  Pipemaster.Chain.chainFrag_run
  Pipemaster.Chain.chainFrag_adapters_iff
  Pipemaster.Chain.chain_roundtrip
  Pipemaster.Chain.chain_gha_gitlab_gitea
)
required_lean="$(printf '`%s, ' "${required[@]}")"
required_lean="[${required_lean%, }]"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/Axioms.tpl" <<'LEAN'
import Pipemaster
import Lean
open Lean Elab Command

#eval show CommandElabM Unit from do
  let env ← getEnv
  let allowed : List Name := [``propext, ``Classical.choice, ``Quot.sound]
  let required : List Name := @REQUIRED@
  let mut checked : Nat := 0
  let mut bad : Array String := #[]
  for r in required do
    match env.find? r with
    | some (.thmInfo _) => pure ()
    | _ => bad := bad.push s!"{r}: required theorem missing"
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
  if checked == 0 then
    bad := bad.push "no Pipemaster declarations checked"
  if bad.isEmpty then
    logInfo m!"PASS: {checked} declarations use only propext, Classical.choice, Quot.sound"
  else
    throwError "FAIL:\n{"\n".intercalate bad.toList}"
LEAN
sed "s/@REQUIRED@/${required_lean}/" "$tmp/Axioms.tpl" >"$tmp/Axioms.lean"

out="$(lake env lean "$tmp/Axioms.lean" 2>&1)" || { echo "$out"; exit 1; }
echo "$out" | grep -v "module system" | grep -q "PASS" || { echo "$out"; exit 1; }
echo "$out" | grep "PASS"
