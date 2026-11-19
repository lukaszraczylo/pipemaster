import Pipemaster.RoundTrip

namespace Pipemaster
namespace Chain

open Core

/-! Chained conversions. A chain converts a pipeline through a list of target providers. Each hop emits the
current pipeline, parses the files back and records the metadata of the pipeline it emitted (rule 4 of
`roundtrip.Hop`, which pushes a layer). Converting back to the origin unwinds the layers from the top (rule 3).
`exact` is the per-provider round trip, `restore (parse (emit q)) (metaOf q) = q`, on the fragment `Frag`.

Gitea and Forgejo are dialects of the GHA adapter (provider/gha/dialect.go). The core subset has no field that a
dialect treats differently: the dialect tables change the emit directory, the default action host, the accepted
`on:` events (push and pull_request are accepted by all three), the secret and variable name prefixes,
the unsupported `environment` and `permissions` keys, the Forgejo default concurrency cancel, and the OIDC key.
None of them touches a job, a step, an env var, a runner label, a timeout, a retry, a need or a push, tag or
pr trigger. Both therefore reuse `Gha.rawGha_toGha`. The vectors check this claim against the Go dialects. -/

/-- A provider adapter on the core subset (Mirrors provider.Provider (provider/provider.go)). `parse` is the parser
before the metadata is applied; `metaOf q` is the layer that `roundtrip.NewMeta` stores when `q` is emitted. -/
structure Adapter where
  Ast : Type
  emit : Pipe → Ast
  parse : Ast → Pipe
  metaOf : Pipe → Meta
  Frag : Pipe → Prop
  exact : ∀ q, Frag q → restore (parse (emit q)) (metaOf q) = q

/-- Mirrors gha.GHA (provider/gha/dialect.go). -/
def gha : Adapter where
  Ast := Gha.Workflow
  emit := Gha.toGha
  parse := Gha.rawGha
  metaOf := Core.metaOf
  Frag := InGha
  exact q h := by
    rw [Gha.rawGha_toGha q h.1]
    exact restore_metaOf_self q h.1.2.1 fun j hj => (h.1.2.2.2.2.2.2.2.1 j hj).2

/-- Mirrors gha.Gitea (provider/gha/dialect.go): the GHA model, because no core field differs (see above). -/
def gitea : Adapter := gha

/-- Mirrors gha.Forgejo (provider/gha/dialect.go): the GHA model, because no core field differs (see above). -/
def forgejo : Adapter := gha

/-- Mirrors gitlab.Parse (provider/gitlab/parse.go). -/
def gitlab : Adapter where
  Ast := Gitlab.Pipeline
  emit := Gitlab.toGitlab
  parse := Gitlab.rawGitlab
  metaOf := Core.metaOf
  Frag := InGitlab
  exact q h := Gitlab.gitlab_from_to q h

/-- Mirrors roundtrip.Hop (internal/roundtrip/chain.go), rule 4: each hop emits, parses and pushes a layer.
The layers are oldest first, as in `meta.Stack`. -/
def hops : Pipe → List Adapter → Pipe × List Meta
  | p, [] => (p, [])
  | p, a :: as =>
    let r := hops (a.parse (a.emit p)) as
    (r.1, a.metaOf p :: r.2)

/-- Mirrors roundtrip.Hop (internal/roundtrip/chain.go), rule 3: restore the top layer first, then the older ones. -/
def unwind : Pipe → List Meta → Pipe
  | q, [] => q
  | q, m :: ms => restore (unwind q ms) m

/-- Each adapter's fragment holds for the pipeline it receives. -/
def ChainFrag : Pipe → List Adapter → Prop
  | _, [] => True
  | p, a :: as => a.Frag p ∧ ChainFrag (a.parse (a.emit p)) as

theorem chain_roundtrip (p : Pipe) (as : List Adapter) (h : ChainFrag p as) :
    unwind (hops p as).1 (hops p as).2 = p := by
  induction as generalizing p with
  | nil => rfl
  | cons a as ih =>
    obtain ⟨hf, hc⟩ := h
    simp only [hops, unwind]
    rw [ih _ hc]
    exact a.exact p hf

/-- The ID the GitLab parser assigns is a valid GHA job key. -/
theorem validId_gitlabPipelineId : validId gitlabPipelineId := by
  have hl : "gitlab-ci".toLower = "gitlab-ci" := by decide +kernel
  have ht : "gitlab-ci".toList = ['g','i','t','l','a','b','-','c','i'] := by decide +kernel
  refine ⟨?_, ?_, ?_, ?_⟩
  · show "gitlab-ci".toList ≠ []
    rw [ht]; simp
  · show ∀ c ∈ "gitlab-ci".toList.take 1, c.isAlpha ∨ c = '_'
    rw [ht]
    intro c hc
    simp only [List.take, List.mem_singleton] at hc
    subst hc; left; decide +kernel
  · show ∀ c ∈ "gitlab-ci".toList, c.isAlphanum ∨ c = '_' ∨ c = '-'
    rw [ht]
    intro c hc
    simp only [List.mem_cons, List.not_mem_nil, or_false] at hc
    rcases hc with rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl
    all_goals first | (left; decide +kernel) | (right; right; rfl)
  · show "gitlab-ci".toLower ∉ gitlabReserved
    rw [hl]; decide +kernel

theorem strip_needs_job (j : Core.Job) : ((Gitlab.strip j).needs.map (·.job)) = j.needs.map (·.job) := by
  simp [Gitlab.strip, List.map_map, Function.comp_def]

theorem dagJobs_strip (js : List Core.Job) : dagJobs (js.map Gitlab.strip) = dagJobs js := by
  unfold dagJobs
  simp [Gitlab.strip, List.map_map, Function.comp_def]

theorem jobIds_strip (js : List Core.Job) : jobIds (js.map Gitlab.strip) = jobIds js := by
  simp [jobIds, Gitlab.strip, List.map_map, Function.comp_def]

/-- The pipeline GitLab hands to the next hop (no metadata applied) still lies in the GHA fragment. -/
theorem inGha_rawGitlab (p : Pipe) (h : InGitlab p) : InGha (Gitlab.rawGitlab (Gitlab.toGitlab p)) := by
  obtain ⟨hw, hg, ha⟩ := h
  rw [Gitlab.rawGitlab_toGitlab p hw hg]
  obtain ⟨h0, h1, h2, h3, h4, h5, h6, h7, h8, h9, h10⟩ := hw
  obtain ⟨hany, hretry, hfree⟩ := hg
  have mem : ∀ x, x ∈ (p.jobs.map Gitlab.strip ++ p.fin.map Gitlab.strip) ↔ ∃ j ∈ p.all, Gitlab.strip j = x := by
    intro x; simp [Pipe.all, List.mem_append, List.mem_map, or_and_right, exists_or]
  have allm : ∀ x ∈ p.jobs.map Gitlab.strip ++ p.fin.map Gitlab.strip, ∃ j ∈ p.all, Gitlab.strip j = x :=
    fun x hx => (mem x).1 hx
  have strip_needs_nil : ∀ j : Core.Job, (Gitlab.strip j).needs = [] ↔ j.needs = [] := by
    intro j; simp [Gitlab.strip]
  refine ⟨⟨?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_⟩, ?_, ?_⟩
  · exact validId_gitlabPipelineId
  · show (jobIds (p.jobs.map Gitlab.strip ++ p.fin.map Gitlab.strip)).Nodup
    have : jobIds (p.jobs.map Gitlab.strip ++ p.fin.map Gitlab.strip) = jobIds p.all := by
      simp only [jobIds, Pipe.all, List.map_append]
      rw [← jobIds, ← jobIds, jobIds_strip, jobIds_strip]; simp [jobIds]
    rw [this]; exact h1
  · intro x hx
    obtain ⟨j, hj, rfl⟩ := allm x hx
    exact h2 j hj
  · show (Dag.layers (dagJobs (p.jobs.map Gitlab.strip))).isSome = true
    rw [dagJobs_strip]; exact h3
  · intro x hx n hn
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 hx
    obtain ⟨n0, hn0, rfl⟩ := List.mem_map.1 (show n ∈ j.needs.map _ from hn)
    show n0.job ∈ jobIds (p.jobs.map Gitlab.strip)
    rw [jobIds_strip]; exact h4 j hj n0 hn0
  · intro x hx n hn
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 hx
    obtain ⟨n0, hn0, rfl⟩ := List.mem_map.1 (show n ∈ j.needs.map _ from hn)
    show n0.job ∈ jobIds (p.jobs.map Gitlab.strip)
    rw [jobIds_strip]; exact h5 j hj n0 hn0
  · intro x hx
    obtain ⟨j, hj, rfl⟩ := allm x hx
    rw [strip_needs_job]; exact h6 j hj
  · intro x hx
    obtain ⟨j, hj, rfl⟩ := allm x hx
    refine ⟨(h7 j hj).1, ?_⟩
    have := (h7 j hj).2
    simpa [Gitlab.strip, List.map_map, Function.comp_def] using this
  · intro x hx s hs
    obtain ⟨j, hj, rfl⟩ := allm x hx
    obtain ⟨s0, hs0, rfl⟩ := List.mem_map.1 (show s ∈ j.steps.map _ from hs)
    exact h8 j hj s0 hs0
  · intro x hx
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 hx
    exact h9 j hj
  · intro x hx
    obtain ⟨j, hj, rfl⟩ := List.mem_map.1 hx
    exact h10 j hj
  · intro x hx
    obtain ⟨j, hj, rfl⟩ := allm x hx
    obtain ⟨hr, hst, hsc⟩ := ha j hj
    refine ⟨hr, ?_, ?_⟩
    · simpa [Gitlab.strip] using hst
    · intro s hs
      obtain ⟨s0, hs0, rfl⟩ := List.mem_map.1 (show s ∈ j.steps.map _ from hs)
      exact hsc s0 hs0
  · refine ⟨hany, ?_, ?_⟩
    · intro x hx
      obtain ⟨j, hj, rfl⟩ := allm x hx
      exact hretry j hj
    · intro x hx hnil
      obtain ⟨j, hj, rfl⟩ := List.mem_map.1 hx
      intro y hy
      obtain ⟨k, hk, rfl⟩ := allm y hy
      have := hfree j hj ((strip_needs_nil j).1 hnil) k hk
      exact (strip_needs_nil k).2 this

/-- The four adapters. -/
def adapters : List Adapter := [gha, gitea, forgejo, gitlab]

/-- After one hop through any adapter, a GHA-fragment pipeline stays in the fragment of every adapter. -/
theorem chainFrag_pair (p : Pipe) (h : InGha p) (b c : Adapter) (hb : b ∈ adapters) (hc : c ∈ adapters) :
    ChainFrag p [b, c] := by
  have hl : InGitlab p := ⟨h.1, h.2.2, h.2.1⟩
  have hr : InGha (Gitlab.rawGitlab (Gitlab.toGitlab p)) := inGha_rawGitlab p hl
  have hrl : InGitlab (Gitlab.rawGitlab (Gitlab.toGitlab p)) := ⟨hr.1, hr.2.2, hr.2.1⟩
  have hp : Gha.rawGha (Gha.toGha p) = p := Gha.rawGha_toGha p h.1
  have key : ∀ a ∈ adapters, a.Frag p ∧ a.Frag (Gitlab.rawGitlab (Gitlab.toGitlab p)) ∧
      (a.parse (a.emit p) = p ∨ a.parse (a.emit p) = Gitlab.rawGitlab (Gitlab.toGitlab p)) := by
    intro a ha
    simp only [adapters, List.mem_cons, List.not_mem_nil, or_false] at ha
    rcases ha with rfl | rfl | rfl | rfl
    · exact ⟨h, hr, Or.inl hp⟩
    · exact ⟨h, hr, Or.inl hp⟩
    · exact ⟨h, hr, Or.inl hp⟩
    · exact ⟨hl, hrl, Or.inr rfl⟩
  refine ⟨(key b hb).1, ?_, trivial⟩
  rcases (key b hb).2.2 with e | e <;> rw [e]
  · exact (key c hc).1
  · exact (key c hc).2.1

theorem chain_gha_gitlab_gitea (p : Pipe) (h : InGha p) :
    unwind (hops p [gitlab, gitea]).1 (hops p [gitlab, gitea]).2 = p := by
  apply chain_roundtrip
  have hl : InGitlab p := ⟨h.1, h.2.2, h.2.1⟩
  exact ⟨hl, inGha_rawGitlab p hl, trivial⟩

end Chain
end Pipemaster
