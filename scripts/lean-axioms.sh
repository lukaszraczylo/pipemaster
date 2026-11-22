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
  Pipemaster.Woodpecker.wpView_set
  Pipemaster.Woodpecker.lower_ne_empty
  Pipemaster.Woodpecker.modeTest_empty
  Pipemaster.Woodpecker.matchV_unset
  Pipemaster.Woodpecker.eq_empty_differs
  Pipemaster.Woodpecker.eq_null_differs
  Pipemaster.Woodpecker.not_gitlab_class
  Pipemaster.Woodpecker.eval_woodpecker
  Pipemaster.Woodpecker.woodpecker_sound
  Pipemaster.Woodpecker.woodpecker_event_rx
  Pipemaster.Woodpecker.rawWp_toWp
  Pipemaster.Woodpecker.rawWp_frag
  Pipemaster.Woodpecker.woodpecker_from_to
  Pipemaster.Woodpecker.restoreJob_wpMain
  Pipemaster.Woodpecker.restoreJob_wpFin
  Pipemaster.Woodpecker.wpMeta_fields
  Pipemaster.Woodpecker.validId_wpPipelineId
  Pipemaster.Drone.unesc_esc
  Pipemaster.Drone.pureOf_trigBlock
  Pipemaster.Drone.pickShared_ok
  Pipemaster.Drone.readTriggers_toDrone
  Pipemaster.Drone.rawDrone_toDrone
  Pipemaster.Drone.rawDrone_frag
  Pipemaster.Drone.restoreJob_droneMain
  Pipemaster.Drone.restoreJob_droneFin
  Pipemaster.Drone.droneMeta_fields
  Pipemaster.Drone.validId_dronePipelineId
  Pipemaster.Drone.drone_from_to
  Pipemaster.Drone.foldOk_emitted
  Pipemaster.Drone.foldOk_edited
  Pipemaster.Drone.cellDocCount_eq
  Pipemaster.Drone.cell_origin
  Pipemaster.Drone.valueE_eval
  Pipemaster.Drone.orChain_eval
  Pipemaster.Drone.filterE_eval
  Pipemaster.Drone.toE_eval
  Pipemaster.RoundTrip.cross_roundtrip
  Pipemaster.RoundTrip.cross_roundtrip_rev
  Pipemaster.RoundTrip.both_iff
  Pipemaster.Core.fromGitlab_restore
  Pipemaster.Core.metaOf_runner
  Pipemaster.Core.metaOf_retry
  Pipemaster.Core.metaOf_timeout
  Pipemaster.Chain.adapters_closed
  Pipemaster.Chain.chainFrag_run
  Pipemaster.Chain.chainFrag_adapters_iff
  Pipemaster.Chain.chain_roundtrip
  Pipemaster.Chain.chain_gha_gitlab_gitea
  Pipemaster.Chain.gitlab_keeps_woodpecker
  Pipemaster.Chain.woodpecker_keeps_woodpecker
  Pipemaster.Chain.woodpecker_keeps_gitlab
  Pipemaster.Chain.woodpecker_keeps_gha
  Pipemaster.Chain.chain_woodpecker_gha
  Pipemaster.Chain.chain_gha_woodpecker_gitlab
  Pipemaster.Chain.gitlab_keeps_drone
  Pipemaster.Chain.woodpecker_keeps_drone
  Pipemaster.Chain.drone_keeps_drone
  Pipemaster.Chain.drone_keeps_woodpecker
  Pipemaster.Chain.drone_keeps_gitlab
  Pipemaster.Chain.drone_keeps_gha
  Pipemaster.Chain.chain_drone_gha
  Pipemaster.Chain.chain_gha_drone_glb
  Pipemaster.Chain.chain_gitlab_drone_woodpecker
  Pipemaster.Chain.cross_drone_gha
  Pipemaster.Chain.cross_drone_gitlab
  Pipemaster.Tekton.rawTekton_toTekton
  Pipemaster.Tekton.tekton_from_to
  Pipemaster.Tekton.clearP_preserves
  Pipemaster.Tekton.tekOk_of_shape
  Pipemaster.Tekton.eval_in_fold
  Pipemaster.Tekton.eval_notin_fold
  Pipemaster.Tekton.when_decode
  Pipemaster.Chain.tekton_keeps_gha
  Pipemaster.Chain.tekton_keeps_gitlab
  Pipemaster.Chain.tekton_keeps_woodpecker
  Pipemaster.Chain.tekton_keeps_drone
  Pipemaster.Chain.tekton_keeps_tekton
  Pipemaster.Chain.gitlab_keeps_tekton
  Pipemaster.Chain.woodpecker_keeps_tekton
  Pipemaster.Chain.drone_keeps_tekton
  Pipemaster.Chain.chain_tekton_gha
  Pipemaster.Chain.chain_gha_tekton_gitlab
  Pipemaster.Chain.chain_gitlab_drone_tekton_woodpecker
  Pipemaster.Chain.cross_tekton_gha
  Pipemaster.Chain.cross_tekton_gitlab
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
