import Pipemaster.Gha
import Pipemaster.Gitlab

namespace Pipemaster
namespace RoundTrip

open Core

/-! Theorems 1 and 2 hold per adapter (`Gha.gha_from_to`, `Gitlab.gitlab_from_to`); these two compose them across
the providers. The model covers the core subset of the IR (Go `ir.CoreOf`, ir/core.go) and nothing else.

Covered: one pipeline with at most two main jobs and two finally jobs in the vectors (the theorems hold for any
number), `needs` with the artifacts flag, manual, allow_failure, retry, timeout in whole minutes, literal job and
step env, runner labels, up to two script steps per job in the vectors, step and job and pipeline display names,
push, tag and pr triggers with branch or tag include lists, and finally jobs that run always or only on failure.

Left out, each on purpose:
- Expressions (conditions, interpolation): Task 15 proves their translation (`Translate.tr_sound`).
- Matrices: Task 10 (`Matrix.expand_mem`).
- Actions, services, containers, caches, artifacts, outputs, environments, permissions, concurrency, inputs.
- Schedule, manual and API triggers, path filters, exclude filters.
- Optional needs, retry conditions, step options other than env, shells, working directories.
- The `Ext` passthrough of every node: `ir.ToCore` ignores it, so the theorems say nothing about provider
  extension blobs.
- The text of `workflow:rules` and of the shell wrappers: the abstract ASTs hold the structure those texts encode,
  and the Go vector tests decode the emitted text into the same shape. String quoting is therefore a codec
  checked on the vector alphabet only.

`metaOf` models what the round-trip metadata of Go carries and nothing else: the display names (`meta.Residual`)
and the needs list of a job whose artifacts flag GitLab cannot keep (`meta.Lost`, recorded by the GitLab emitter
since Task 23). The pipeline ID that GitLab renames is added by `Gitlab.gitlabMeta`. The fragments `GhaFrag` and
`GitlabFrag` come from the capability matrices (provider/gha/caps.go, provider/gitlab/caps.go): GitLab caps retry at three runs, and GHA
needs a script per job. A job with no runner label passes GHA, because the emitter writes the default label and the parser removes it again (provider/gha/emit_job.go:320-322, collapse.go:48-50).

A manual finally job is outside the fragment because it is a reported loss, not a silent one. The GitLab
emitter (Mirrors gitlab.timing (provider/gitlab/emit_rules.go)) lets `when: manual` win over the
finally branch and records a `lost` entry for `pipeline.finally` at the job path; the parser then returns a main
job. The GHA emitter keeps both guards (`always() && github.event_name == 'workflow_dispatch'`) and the parser
restores the job, but the model `Gha.finJob` does not state that, so `Wf` excludes the case for both providers. -/

/-- The pipelines both providers accept. The two cross theorems run through both, so they assume both fragments. -/
def Both (p : Pipe) : Prop := GhaFrag p ∧ GitlabFrag p

instance (p : Pipe) : Decidable (Both p) := by unfold Both; exact inferInstance

/-- `Both` is the fragment the cross theorems assumed before the fragments were split (the GHA fragment with
GitLab's needs added, and the GitLab fragment with GHA's needs added). -/
theorem both_iff (p : Pipe) : Both p ↔ (Wf p ∧ GhaOk p ∧ GitlabOk p) ∧ (Wf p ∧ GitlabOk p ∧ GhaOk p) := by
  unfold Both GhaFrag GitlabFrag
  constructor
  · rintro ⟨⟨w, a⟩, _, g⟩; exact ⟨⟨w, a, g⟩, w, g, a⟩
  · rintro ⟨⟨w, a, g⟩, _⟩; exact ⟨⟨w, a⟩, w, g⟩

/-- A pipeline that survives GHA, then GitLab, then GHA again. -/
theorem cross_roundtrip (p : Pipe) (h : Both p) :
    Gha.fromGha (Gha.toGha (Gitlab.fromGitlab (Gitlab.toGitlab p) (Gitlab.gitlabMeta p))) = p := by
  have hg : Gitlab.fromGitlab (Gitlab.toGitlab p) (Gitlab.gitlabMeta p) = p :=
    Gitlab.gitlab_from_to p h.2
  rw [hg]
  exact Gha.gha_from_to p h.1

/-- A pipeline that survives GitLab, then GHA, then GitLab again. -/
theorem cross_roundtrip_rev (p : Pipe) (h : Both p) :
    Gitlab.fromGitlab (Gitlab.toGitlab (Gha.fromGha (Gha.toGha p) (metaOf p))) (Gitlab.gitlabMeta p) = p := by
  have hg : Gha.fromGha (Gha.toGha p) (metaOf p) = p := by
    unfold Gha.fromGha
    rw [Gha.rawGha_toGha p h.1.1]
    exact restore_metaOf_self p h.1.1.2.1 fun j hj => (h.1.1.2.2.2.2.2.2.2.1 j hj).2
  rw [hg]
  exact Gitlab.gitlab_from_to p h.2

end RoundTrip
end Pipemaster
