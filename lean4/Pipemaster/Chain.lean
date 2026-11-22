import Pipemaster.RoundTrip
import Pipemaster.Woodpecker
import Pipemaster.Drone
import Pipemaster.Tekton

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
before the metadata is applied; `metaOf q` is the layer that `roundtrip.NewMeta` stores when `q` is emitted.
`Own` is what the adapter needs beyond the shared structure `Wf`; the adapter's fragment is `Wf` and `Own`. -/
structure Adapter where
  Ast : Type
  emit : Pipe → Ast
  parse : Ast → Pipe
  metaOf : Pipe → Meta
  Own : Pipe → Prop
  decOwn : DecidablePred Own
  exact : ∀ q, Wf q → Own q → restore (parse (emit q)) (metaOf q) = q

/-- The fragment of an adapter: the pipelines its round trip is proved for. -/
def Adapter.Frag (a : Adapter) (p : Pipe) : Prop := Wf p ∧ a.Own p

instance (a : Adapter) (p : Pipe) : Decidable (a.Frag p) := by
  unfold Adapter.Frag; exact @instDecidableAnd _ _ inferInstance (a.decOwn p)

/-- A hop through `a` keeps a pipeline of `a`'s fragment inside the fragment of `b`, when it was already there. -/
def Adapter.Keeps (a b : Adapter) : Prop := ∀ q, a.Frag q → b.Frag q → b.Frag (a.parse (a.emit q))

/-- A hop that returns the pipeline it received keeps every fragment. -/
theorem Adapter.keeps_of_id (a : Adapter) (h : ∀ q, a.Frag q → a.parse (a.emit q) = q) (b : Adapter) :
    a.Keeps b := by
  intro q hq hb
  rw [h q hq]; exact hb

/-- Mirrors gha.GHA (provider/gha/dialect.go). -/
def gha : Adapter where
  Ast := Gha.Workflow
  emit := Gha.toGha
  parse := Gha.rawGha
  metaOf := Core.metaOf
  Own := GhaOk
  decOwn := fun _ => inferInstance
  exact q hw _ := by
    rw [Gha.rawGha_toGha q hw]
    exact restore_metaOf_self q hw.2.1 fun j hj => (hw.2.2.2.2.2.2.2.1 j hj).2

/-- Mirrors gha.Gitea (provider/gha/dialect.go): the GHA model, because no core field differs (see above). -/
def gitea : Adapter := gha

/-- Mirrors gha.Forgejo (provider/gha/dialect.go): the GHA model, because no core field differs (see above). -/
def forgejo : Adapter := gha

/-- Mirrors gitlab.Parse (provider/gitlab/parse.go). -/
def gitlab : Adapter where
  Ast := Gitlab.Pipeline
  emit := Gitlab.toGitlab
  parse := Gitlab.rawGitlab
  metaOf := Gitlab.gitlabMeta
  Own := GitlabOk
  decOwn := fun _ => inferInstance
  exact q hw hg := Gitlab.gitlab_from_to q ⟨hw, hg⟩

/-- Mirrors woodpecker.New (provider/woodpecker/dialect.go). -/
def woodpecker : Adapter where
  Ast := List Woodpecker.WFile
  emit := Woodpecker.toWp
  parse := Woodpecker.rawWp
  metaOf := Woodpecker.wpMeta
  Own := Woodpecker.WpOk
  decOwn := fun _ => inferInstance
  exact q hw ho := Woodpecker.woodpecker_from_to q ⟨hw, ho⟩

/-- Mirrors drone.New (provider/drone/dialect.go). -/
def drone : Adapter where
  Ast := List Drone.DDoc
  emit := Drone.toDrone
  parse := Drone.rawDrone
  metaOf := Drone.droneMeta
  Own := Drone.DroneOk
  decOwn := fun _ => inferInstance
  exact q hw ho := Drone.drone_from_to q ⟨hw, ho⟩

/-- Mirrors tekton.New (provider/tekton/dialect.go). -/
def tekton : Adapter where
  Ast := Tekton.TDoc
  emit := Tekton.toTekton
  parse := Tekton.rawTekton
  metaOf := Tekton.tektonMeta
  Own := Tekton.TekOk
  decOwn := fun _ => inferInstance
  exact q hw ho := Tekton.tekton_from_to q ⟨hw, ho⟩

/-- Mirrors roundtrip.Hop (internal/roundtrip/chain.go), rule 4: each hop emits, parses and pushes a layer.
The layers are oldest first, as in `meta.Stack`. `Hop` applies a stack only if its top layer targets the source
provider and lists every file read. A stack built by `hops` meets that precondition: each layer is pushed for the
files its own hop emitted, and the next hop reads exactly those files. The model has no `Target` field because
the precondition always holds here. -/
def hops : Pipe → List Adapter → Pipe × List Meta
  | p, [] => (p, [])
  | p, a :: as =>
    let r := hops (a.parse (a.emit p)) as
    (r.1, a.metaOf p :: r.2)

/-- Mirrors roundtrip.Hop (internal/roundtrip/chain.go), rule 3: restore the top layer first, then the older ones.
The layers come from `hops`, so they satisfy the accepted-stack precondition of `Hop` (see `hops`). -/
def unwind : Pipe → List Meta → Pipe
  | q, [] => q
  | q, m :: ms => restore (unwind q ms) m

/-- Each adapter's fragment holds for the pipeline it receives. -/
def RunFrag : Pipe → List Adapter → Prop
  | _, [] => True
  | p, a :: as => a.Frag p ∧ RunFrag (a.parse (a.emit p)) as

/-- The intersection of the fragments of the adapters in a chain, all taken at the origin pipeline. -/
def ChainFrag (p : Pipe) (as : List Adapter) : Prop := ∀ a ∈ as, a.Frag p

instance (p : Pipe) (as : List Adapter) : Decidable (ChainFrag p as) := by
  unfold ChainFrag; exact inferInstance

theorem chain_roundtrip (p : Pipe) (as : List Adapter) (h : RunFrag p as) :
    unwind (hops p as).1 (hops p as).2 = p := by
  induction as generalizing p with
  | nil => rfl
  | cons a as ih =>
    obtain ⟨hf, hc⟩ := h
    simp only [hops, unwind]
    rw [ih _ hc]
    exact a.exact p hf.1 hf.2

/-- The ID the GitLab parser assigns is a valid GHA job key. -/
theorem validId_gitlabPipelineId : validId Gitlab.gitlabPipelineId := by
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

/-- The pipeline GitLab hands to the next hop (no metadata applied) keeps the shared structure and GitLab's own
needs, and keeps GHA's needs when the input meets them. -/
theorem rawGitlab_frag (p : Pipe) (hw : Wf p) (hg : GitlabOk p) :
    Wf (Gitlab.rawGitlab (Gitlab.toGitlab p)) ∧ (GhaOk p → GhaOk (Gitlab.rawGitlab (Gitlab.toGitlab p))) ∧
    GitlabOk (Gitlab.rawGitlab (Gitlab.toGitlab p)) := by
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
  · intro ha x hx
    obtain ⟨j, hj, rfl⟩ := allm x hx
    obtain ⟨hst, hsc⟩ := ha j hj
    refine ⟨?_, ?_⟩
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

/-- A list of adapters is closed when a hop through any member keeps a pipeline in the fragment of every member.
Adding an adapter to a list means proving `Keeps` for the new pairs. -/
def Closed (L : List Adapter) : Prop := ∀ a ∈ L, ∀ b ∈ L, a.Keeps b

theorem gha_keeps (b : Adapter) : gha.Keeps b :=
  Adapter.keeps_of_id _ (fun q hq => Gha.rawGha_toGha q hq.1) b

theorem gitlab_keeps_gha : gitlab.Keeps gha := by
  intro q hq hg
  have h := rawGitlab_frag q hq.1 hq.2
  exact ⟨h.1, h.2.1 hg.2⟩

theorem gitlab_keeps_gitlab : gitlab.Keeps gitlab := by
  intro q hq _
  have h := rawGitlab_frag q hq.1 hq.2
  exact ⟨h.1, h.2.2⟩

/-! ## Woodpecker in a chain

The Woodpecker parser returns every job with no runner label, or with its `os/arch` platform label alone
(`Woodpecker.platformOf`). Only the round-trip metadata brings the labels back (`Woodpecker.wpMeta`), and only at
the layer's own target (internal/roundtrip/chain.go:45-62). A hop through GHA after a hop through Woodpecker
therefore sees a job with no runner. The GHA emitter writes the default label `ubuntu-latest` with an emulated
`job.runner` marker (provider/gha/emit_job.go:320-322) and its parser removes the default again
(provider/gha/collapse.go:48-50), so `GhaOk` does not need a runner and the empty runner survives the hop. -/

theorem gitlab_keeps_woodpecker : gitlab.Keeps woodpecker := by
  intro q hq hw
  have hraw := Gitlab.rawGitlab_toGitlab q hq.1 hq.2
  have hf := rawGitlab_frag q hq.1 hq.2
  refine ⟨hf.1, ?_⟩
  show Woodpecker.WpOk (Gitlab.rawGitlab (Gitlab.toGitlab q))
  rw [hraw]
  obtain ⟨hm, hx, hne, hjo⟩ := hw.2
  have allm : ∀ x ∈ q.jobs.map Gitlab.strip ++ q.fin.map Gitlab.strip, ∃ j ∈ q.all, Gitlab.strip j = x := by
    intro x hx
    simp only [List.mem_append, List.mem_map] at hx
    rcases hx with ⟨j, hj, rfl⟩ | ⟨j, hj, rfl⟩
    · exact ⟨j, by simp [Pipe.all, hj], rfl⟩
    · exact ⟨j, by simp [Pipe.all, hj], rfl⟩
  refine ⟨?_, hx, ?_, ?_⟩
  · intro x hx hman
    obtain ⟨j, hj, rfl⟩ := allm x hx
    exact hm j hj hman
  · intro h
    apply hne
    simp only [Pipe.all, List.append_eq_nil_iff, List.map_eq_nil_iff] at h ⊢
    exact h
  · intro x hx
    obtain ⟨j, hj, rfl⟩ := allm x hx
    refine ⟨(hjo j hj).1, ?_⟩
    intro s hs'
    obtain ⟨s0, hs0, rfl⟩ := List.mem_map.1 (show s ∈ j.steps.map _ from hs')
    exact (hjo j hj).2 s0 hs0

theorem woodpecker_keeps_woodpecker : woodpecker.Keeps woodpecker := by
  intro q hq _
  have h := Woodpecker.rawWp_frag q hq.1 hq.2
  exact ⟨h.1, h.2.1⟩

theorem woodpecker_keeps_gitlab : woodpecker.Keeps gitlab := by
  intro q hq hg
  have h := Woodpecker.rawWp_frag q hq.1 hq.2
  exact ⟨h.1, h.2.2.2 hg.2⟩

theorem woodpecker_keeps_gha : woodpecker.Keeps gha := by
  intro q hq hg
  have h := Woodpecker.rawWp_frag q hq.1 hq.2
  exact ⟨h.1, h.2.2.1 hg.2⟩

/-! ## Drone in a chain

The Drone parser returns every job with no runner label, or with the `os/arch` part of its platform label
(`Drone.droneStripMain`), and keeps no name. The raw parse differs from the Woodpecker one only in the runner and
the pipeline ID, so it keeps every fragment the same way (`Drone.frag_of_shape`). -/

theorem keep_strip (j : Core.Job) : Drone.Keep j (Gitlab.strip j) :=
  ⟨rfl, strip_needs_job j, rfl, rfl, rfl, rfl, by simp [Gitlab.strip], fun h => h⟩

theorem keep_of_same {j x : Core.Job} (h : Woodpecker.Same j x) : Drone.Keep j x :=
  ⟨h.1, h.2.1, h.2.2.1, h.2.2.2.1, h.2.2.2.2.1, h.2.2.2.2.2.1, h.2.2.2.2.2.2.1, fun _ => h.2.2.2.2.2.2.2⟩

theorem gitlab_keeps_drone : gitlab.Keeps drone := by
  intro q hq hd
  have hraw := Gitlab.rawGitlab_toGitlab q hq.1 hq.2
  have hf := rawGitlab_frag q hq.1 hq.2
  refine ⟨hf.1, ?_⟩
  exact (Drone.frag_of_shape q Gitlab.gitlabPipelineId validId_gitlabPipelineId Gitlab.strip Gitlab.strip
    keep_strip keep_strip _ hraw).2.2.1 hd.2

theorem woodpecker_keeps_drone : woodpecker.Keeps drone := by
  intro q hq hd
  have hraw := Woodpecker.rawWp_toWp q hq.1 hq.2
  have hf := Woodpecker.rawWp_frag q hq.1 hq.2
  refine ⟨hf.1, ?_⟩
  exact (Drone.frag_of_shape q Woodpecker.wpPipelineId Woodpecker.validId_wpPipelineId Woodpecker.wpStripMain
    Woodpecker.wpStripFin (fun j => keep_of_same (Woodpecker.same_main j)) (fun j => keep_of_same (Woodpecker.same_fin j))
    _ hraw).2.2.1 hd.2

theorem drone_keeps_drone : drone.Keeps drone := by
  intro q hq _
  have h := Drone.rawDrone_frag q hq.1 hq.2
  exact ⟨h.1, h.2.1⟩

theorem drone_keeps_woodpecker : drone.Keeps woodpecker := by
  intro q hq hw
  have h := Drone.rawDrone_frag q hq.1 hq.2
  exact ⟨h.1, h.2.2.1 hw.2⟩

theorem drone_keeps_gitlab : drone.Keeps gitlab := by
  intro q hq hg
  have h := Drone.rawDrone_frag q hq.1 hq.2
  exact ⟨h.1, h.2.2.2.2 hg.2⟩

theorem drone_keeps_gha : drone.Keeps gha := by
  intro q hq hg
  have h := Drone.rawDrone_frag q hq.1 hq.2
  exact ⟨h.1, h.2.2.2.1 hg.2⟩

/-! ## Tekton in a chain

The Tekton parser returns every job with no runner label and keeps names, retries and timeouts in the file
(`Tekton.rawTekton_toTekton`), so it keeps every fragment (`Tekton.clearP_preserves`). The other adapters keep the
Tekton fragment when the pipeline ID they assign is a short DNS label and their raw parse keeps the retry runs or
drops them (`Tekton.tekOk_of_shape`). The three IDs are `gitlab-ci`, `woodpecker` and `drone`. -/

theorem tekDns_gl :
    Tekton.DnsId Gitlab.gitlabPipelineId ∧ Gitlab.gitlabPipelineId.length ≤ Tekton.maxPipeId := by
  have ht : Gitlab.gitlabPipelineId.toList = ['g', 'i', 't', 'l', 'a', 'b', '-', 'c', 'i'] := by
    unfold Gitlab.gitlabPipelineId; decide +kernel
  refine ⟨?_, ?_⟩
  · refine ⟨by simp [ht], ?_, by simp [ht], by simp [ht]⟩
    intro c hc
    rw [ht] at hc
    simp only [List.mem_cons, List.not_mem_nil, or_false] at hc
    rcases hc with rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl <;> decide
  · rw [← String.length_toList, ht]; decide +kernel

theorem tekDns_wp :
    Tekton.DnsId Woodpecker.wpPipelineId ∧ Woodpecker.wpPipelineId.length ≤ Tekton.maxPipeId := by
  have ht : Woodpecker.wpPipelineId.toList = ['w', 'o', 'o', 'd', 'p', 'e', 'c', 'k', 'e', 'r'] := by
    unfold Woodpecker.wpPipelineId; decide +kernel
  refine ⟨?_, ?_⟩
  · refine ⟨by simp [ht], ?_, by simp [ht], by simp [ht]⟩
    intro c hc
    rw [ht] at hc
    simp only [List.mem_cons, List.not_mem_nil, or_false] at hc
    rcases hc with rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl <;> decide
  · rw [← String.length_toList, ht]; decide +kernel

theorem tekDns_drone :
    Tekton.DnsId Drone.dronePipelineId ∧ Drone.dronePipelineId.length ≤ Tekton.maxPipeId := by
  have ht : Drone.dronePipelineId.toList = ['d', 'r', 'o', 'n', 'e'] := by
    unfold Drone.dronePipelineId; decide +kernel
  refine ⟨?_, ?_⟩
  · refine ⟨by simp [ht], ?_, by simp [ht], by simp [ht]⟩
    intro c hc
    rw [ht] at hc
    simp only [List.mem_cons, List.not_mem_nil, or_false] at hc
    rcases hc with rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl | rfl <;> decide
  · rw [← String.length_toList, ht]; decide +kernel

theorem tekton_keeps_gha : tekton.Keeps gha := by
  intro q hq hg
  have hw := hq.1
  have hraw : Tekton.rawTekton (Tekton.toTekton q) = Tekton.clearP q := Tekton.rawTekton_toTekton q hq.1 hq.2
  have h := Tekton.clearP_preserves q
  show Wf (Tekton.rawTekton (Tekton.toTekton q)) ∧ GhaOk (Tekton.rawTekton (Tekton.toTekton q))
  rw [hraw]
  exact ⟨h.1 hq.1, h.2.1 hg.2⟩

theorem tekton_keeps_gitlab : tekton.Keeps gitlab := by
  intro q hq hg
  have hraw : Tekton.rawTekton (Tekton.toTekton q) = Tekton.clearP q := Tekton.rawTekton_toTekton q hq.1 hq.2
  have h := Tekton.clearP_preserves q
  show Wf (Tekton.rawTekton (Tekton.toTekton q)) ∧ GitlabOk (Tekton.rawTekton (Tekton.toTekton q))
  rw [hraw]
  exact ⟨h.1 hq.1, h.2.2.1 hg.2⟩

theorem tekton_keeps_woodpecker : tekton.Keeps woodpecker := by
  intro q hq hg
  have hraw : Tekton.rawTekton (Tekton.toTekton q) = Tekton.clearP q := Tekton.rawTekton_toTekton q hq.1 hq.2
  have h := Tekton.clearP_preserves q
  show Wf (Tekton.rawTekton (Tekton.toTekton q)) ∧ Woodpecker.WpOk (Tekton.rawTekton (Tekton.toTekton q))
  rw [hraw]
  exact ⟨h.1 hq.1, h.2.2.2.1 hg.2⟩

theorem tekton_keeps_drone : tekton.Keeps drone := by
  intro q hq hg
  have hraw : Tekton.rawTekton (Tekton.toTekton q) = Tekton.clearP q := Tekton.rawTekton_toTekton q hq.1 hq.2
  have h := Tekton.clearP_preserves q
  show Wf (Tekton.rawTekton (Tekton.toTekton q)) ∧ Drone.DroneOk (Tekton.rawTekton (Tekton.toTekton q))
  rw [hraw]
  exact ⟨h.1 hq.1, h.2.2.2.2.1 hg.2⟩

theorem tekton_keeps_tekton : tekton.Keeps tekton := by
  intro q hq hg
  have hraw : Tekton.rawTekton (Tekton.toTekton q) = Tekton.clearP q := Tekton.rawTekton_toTekton q hq.1 hq.2
  have h := Tekton.clearP_preserves q
  show Wf (Tekton.rawTekton (Tekton.toTekton q)) ∧ Tekton.TekOk (Tekton.rawTekton (Tekton.toTekton q))
  rw [hraw]
  exact ⟨h.1 hq.1, h.2.2.2.2.2 hg.2⟩

theorem gitlab_keeps_tekton : gitlab.Keeps tekton := by
  intro q hq ht
  have hraw := Gitlab.rawGitlab_toGitlab q hq.1 hq.2
  have hf := rawGitlab_frag q hq.1 hq.2
  refine ⟨hf.1, ?_⟩
  show Tekton.TekOk (Gitlab.rawGitlab (Gitlab.toGitlab q))
  exact Tekton.tekOk_of_shape q Gitlab.gitlabPipelineId tekDns_gl.1 tekDns_gl.2 Gitlab.strip
    Gitlab.strip keep_strip keep_strip Tekton.tekKeep_strip Tekton.tekKeep_strip _ hraw ht.2

theorem woodpecker_keeps_tekton : woodpecker.Keeps tekton := by
  intro q hq ht
  have hraw := Woodpecker.rawWp_toWp q hq.1 hq.2
  have hf := Woodpecker.rawWp_frag q hq.1 hq.2
  refine ⟨hf.1, ?_⟩
  show Tekton.TekOk (Woodpecker.rawWp (Woodpecker.toWp q))
  exact Tekton.tekOk_of_shape q Woodpecker.wpPipelineId tekDns_wp.1 tekDns_wp.2 Woodpecker.wpStripMain
    Woodpecker.wpStripFin (fun j => keep_of_same (Woodpecker.same_main j)) (fun j => keep_of_same (Woodpecker.same_fin j))
    Tekton.tekKeep_wpMain Tekton.tekKeep_wpFin _ hraw ht.2

theorem drone_keeps_tekton : drone.Keeps tekton := by
  intro q hq ht
  have hraw := Drone.rawDrone_toDrone q hq.1 hq.2
  have hf := Drone.rawDrone_frag q hq.1 hq.2
  refine ⟨hf.1, ?_⟩
  show Tekton.TekOk (Drone.rawDrone (Drone.toDrone q))
  exact Tekton.tekOk_of_shape q Drone.dronePipelineId tekDns_drone.1 tekDns_drone.2 Drone.droneStripMain
    Drone.droneStripFin Drone.keep_droneMain Drone.keep_droneFin Tekton.tekKeep_droneMain Tekton.tekKeep_droneFin _ hraw ht.2

/-- The six adapters of the earlier phases. -/
def sixAdapters : List Adapter := [gha, gitea, forgejo, gitlab, woodpecker, drone]

theorem sixAdapters_closed : Closed sixAdapters := by
  intro a ha b hb
  simp only [sixAdapters, List.mem_cons, List.not_mem_nil, or_false] at ha hb
  rcases ha with rfl | rfl | rfl | rfl | rfl | rfl <;> rcases hb with rfl | rfl | rfl | rfl | rfl | rfl <;>
    first | exact gha_keeps _ | exact gitlab_keeps_gha | exact gitlab_keeps_gitlab |
      exact gitlab_keeps_woodpecker | exact woodpecker_keeps_woodpecker | exact woodpecker_keeps_gitlab |
      exact woodpecker_keeps_gha | exact gitlab_keeps_drone | exact woodpecker_keeps_drone |
      exact drone_keeps_drone | exact drone_keeps_woodpecker | exact drone_keeps_gitlab | exact drone_keeps_gha

/-- The seven adapters. -/
def adapters : List Adapter := [gha, gitea, forgejo, gitlab, woodpecker, drone, tekton]

theorem adapters_closed : Closed adapters := by
  intro a ha b hb
  simp only [adapters, List.mem_cons, List.not_mem_nil, or_false] at ha hb
  rcases ha with rfl | rfl | rfl | rfl | rfl | rfl | rfl <;>
    rcases hb with rfl | rfl | rfl | rfl | rfl | rfl | rfl <;>
    first | exact gha_keeps _ | exact gitlab_keeps_gha | exact gitlab_keeps_gitlab |
      exact gitlab_keeps_woodpecker | exact woodpecker_keeps_woodpecker | exact woodpecker_keeps_gitlab |
      exact woodpecker_keeps_gha | exact gitlab_keeps_drone | exact woodpecker_keeps_drone |
      exact drone_keeps_drone | exact drone_keeps_woodpecker | exact drone_keeps_gitlab | exact drone_keeps_gha |
      exact tekton_keeps_gha | exact tekton_keeps_gitlab | exact tekton_keeps_woodpecker | exact tekton_keeps_drone |
      exact tekton_keeps_tekton | exact gitlab_keeps_tekton | exact woodpecker_keeps_tekton |
      exact drone_keeps_tekton

/-- One hop through a member of a closed list keeps a pipeline in the fragment of every member. -/
theorem chainFrag_hop (L : List Adapter) (hL : Closed L) (p : Pipe) (h : ChainFrag p L) (a : Adapter)
    (ha : a ∈ L) : ChainFrag (a.parse (a.emit p)) L :=
  fun b hb => hL a ha b hb p (h a ha) (h b hb)

/-- A pipeline in the intersection of a closed list passes every chain whose hops come from the list. -/
theorem chainFrag_run (L : List Adapter) (hL : Closed L) (as : List Adapter) (has : ∀ a ∈ as, a ∈ L)
    (p : Pipe) (h : ChainFrag p L) : RunFrag p as := by
  induction as generalizing p with
  | nil => trivial
  | cons a as ih =>
    exact ⟨h a (has a (by simp)), ih (fun b hb => has b (by simp [hb])) _
      (chainFrag_hop L hL p h a (has a (by simp)))⟩

/-- After one hop through any adapter of a closed list, a pipeline in the intersection stays in every fragment. -/
theorem chainFrag_pair (L : List Adapter) (hL : Closed L) (p : Pipe) (h : ChainFrag p L) (b c : Adapter)
    (hb : b ∈ L) (hc : c ∈ L) : RunFrag p [b, c] :=
  chainFrag_run L hL [b, c] (by intro a ha; simp at ha; rcases ha with rfl | rfl <;> assumption) p h

/-- The intersection over the six adapters: both cross fragments, the Woodpecker fragment and the Drone fragment. -/
theorem chainFrag_six_iff (p : Pipe) :
    ChainFrag p sixAdapters ↔ RoundTrip.Both p ∧ woodpecker.Frag p ∧ drone.Frag p := by
  constructor
  · intro h
    exact ⟨⟨h gha (by simp [sixAdapters]), h gitlab (by simp [sixAdapters])⟩, h woodpecker (by simp [sixAdapters]),
      h drone (by simp [sixAdapters])⟩
  · intro h a ha
    simp only [sixAdapters, List.mem_cons, List.not_mem_nil, or_false] at ha
    rcases ha with rfl | rfl | rfl | rfl | rfl | rfl
    · exact h.1.1
    · exact h.1.1
    · exact h.1.1
    · exact h.1.2
    · exact h.2.1
    · exact h.2.2

/-- The intersection over the seven adapters: both cross fragments, and the Woodpecker, Drone and Tekton
fragments. -/
theorem chainFrag_adapters_iff (p : Pipe) :
    ChainFrag p adapters ↔ RoundTrip.Both p ∧ woodpecker.Frag p ∧ drone.Frag p ∧ tekton.Frag p := by
  constructor
  · intro h
    exact ⟨⟨h gha (by simp [adapters]), h gitlab (by simp [adapters])⟩, h woodpecker (by simp [adapters]),
      h drone (by simp [adapters]), h tekton (by simp [adapters])⟩
  · intro h a ha
    simp only [adapters, List.mem_cons, List.not_mem_nil, or_false] at ha
    rcases ha with rfl | rfl | rfl | rfl | rfl | rfl | rfl
    · exact h.1.1
    · exact h.1.1
    · exact h.1.1
    · exact h.1.2
    · exact h.2.1
    · exact h.2.2.1
    · exact h.2.2.2

/-- The adapters without Woodpecker and Drone, which have their own hops. -/
def crossAdapters : List Adapter := [gha, gitea, forgejo, gitlab]

theorem crossAdapters_closed : Closed crossAdapters := by
  intro a ha b hb
  apply adapters_closed a _ b _ <;> simp only [crossAdapters, adapters, List.mem_cons, List.not_mem_nil, or_false] at ha hb ⊢ <;>
    tauto

theorem chain_gha_gitlab_gitea (p : Pipe) (h : RoundTrip.Both p) :
    unwind (hops p [gitlab, gitea]).1 (hops p [gitlab, gitea]).2 = p := by
  apply chain_roundtrip
  apply chainFrag_run crossAdapters crossAdapters_closed _ _ p
  · intro a ha
    simp only [crossAdapters, List.mem_cons, List.not_mem_nil, or_false] at ha
    rcases ha with rfl | rfl | rfl | rfl
    · exact h.1
    · exact h.1
    · exact h.1
    · exact h.2
  · intro a ha
    simp only [List.mem_cons, List.not_mem_nil, or_false] at ha
    simp only [crossAdapters, List.mem_cons, List.not_mem_nil, or_false]
    rcases ha with rfl | rfl <;> simp [gitea, gha, gitlab]

/-- Woodpecker as the first hop, then GHA: the chain the earlier model had to leave out. -/
theorem chain_woodpecker_gha (p : Pipe) (h : RoundTrip.Both p ∧ woodpecker.Frag p) :
    unwind (hops p [woodpecker, gha]).1 (hops p [woodpecker, gha]).2 = p := by
  apply chain_roundtrip
  exact ⟨h.2, woodpecker_keeps_gha p h.2 h.1.1, trivial⟩

/-- GHA, then Woodpecker, then GitLab. -/
theorem chain_gha_woodpecker_gitlab (p : Pipe) (h : RoundTrip.Both p ∧ woodpecker.Frag p) :
    unwind (hops p [gha, woodpecker, gitlab]).1 (hops p [gha, woodpecker, gitlab]).2 = p := by
  apply chain_roundtrip
  have hid : gha.parse (gha.emit p) = p := Gha.rawGha_toGha p h.1.1.1
  refine ⟨h.1.1, ?_⟩
  rw [hid]
  exact ⟨h.2, woodpecker_keeps_gitlab p h.2 h.1.2, trivial⟩

/-- Drone as the first hop, then GHA. -/
theorem chain_drone_gha (p : Pipe) (h : RoundTrip.Both p ∧ drone.Frag p) :
    unwind (hops p [drone, gha]).1 (hops p [drone, gha]).2 = p := by
  apply chain_roundtrip
  exact ⟨h.2, drone_keeps_gha p h.2 h.1.1, trivial⟩

/-- GHA, then Drone, then GitLab. -/
theorem chain_gha_drone_glb (p : Pipe) (h : RoundTrip.Both p ∧ drone.Frag p) :
    unwind (hops p [gha, drone, gitlab]).1 (hops p [gha, drone, gitlab]).2 = p := by
  apply chain_roundtrip
  have hid : gha.parse (gha.emit p) = p := Gha.rawGha_toGha p h.1.1.1
  refine ⟨h.1.1, ?_⟩
  rw [hid]
  exact ⟨h.2, drone_keeps_gitlab p h.2 h.1.2, trivial⟩

/-- GitLab, then Drone, then Woodpecker: the three adapters whose raw parses differ most. -/
theorem chain_gitlab_drone_woodpecker (p : Pipe) (h : RoundTrip.Both p ∧ woodpecker.Frag p ∧ drone.Frag p) :
    unwind (hops p [gitlab, drone, woodpecker]).1 (hops p [gitlab, drone, woodpecker]).2 = p := by
  apply chain_roundtrip
  apply chainFrag_run sixAdapters sixAdapters_closed _ _ p ((chainFrag_six_iff p).2 h)
  intro a ha
  simp only [List.mem_cons, List.not_mem_nil, or_false] at ha
  simp only [sixAdapters, List.mem_cons, List.not_mem_nil, or_false]
  rcases ha with rfl | rfl | rfl <;> simp [gitlab, drone, woodpecker]

/-- The cross round trip with metadata: a pipeline that survives Drone, then GHA, comes back from GHA. -/
theorem cross_drone_gha (p : Pipe) (h : RoundTrip.Both p ∧ drone.Frag p) :
    Gha.fromGha (Gha.toGha (Drone.fromDrone (Drone.toDrone p) (Drone.droneMeta p))) = p := by
  rw [Drone.drone_from_to p h.2]
  exact Gha.gha_from_to p h.1.1

/-- The cross round trip with metadata: a pipeline that survives Drone, then GitLab, comes back from GitLab. -/
theorem cross_drone_gitlab (p : Pipe) (h : RoundTrip.Both p ∧ drone.Frag p) :
    Gitlab.fromGitlab (Gitlab.toGitlab (Drone.fromDrone (Drone.toDrone p) (Drone.droneMeta p)))
      (Gitlab.gitlabMeta p) = p := by
  rw [Drone.drone_from_to p h.2]
  exact Gitlab.gitlab_from_to p h.1.2

/-- Tekton as the first hop, then GHA. -/
theorem chain_tekton_gha (p : Pipe) (h : RoundTrip.Both p ∧ tekton.Frag p) :
    unwind (hops p [tekton, gha]).1 (hops p [tekton, gha]).2 = p := by
  apply chain_roundtrip
  exact ⟨h.2, tekton_keeps_gha p h.2 h.1.1, trivial⟩

/-- GHA, then Tekton, then GitLab. -/
theorem chain_gha_tekton_gitlab (p : Pipe) (h : RoundTrip.Both p ∧ tekton.Frag p) :
    unwind (hops p [gha, tekton, gitlab]).1 (hops p [gha, tekton, gitlab]).2 = p := by
  apply chain_roundtrip
  have hid : gha.parse (gha.emit p) = p := Gha.rawGha_toGha p h.1.1.1
  refine ⟨h.1.1, ?_⟩
  rw [hid]
  exact ⟨h.2, tekton_keeps_gitlab p h.2 h.1.2, trivial⟩

/-- GitLab, then Drone, then Tekton, then Woodpecker: the raw parses that drop the most differ at each hop. -/
theorem chain_gitlab_drone_tekton_woodpecker (p : Pipe)
    (h : RoundTrip.Both p ∧ woodpecker.Frag p ∧ drone.Frag p ∧ tekton.Frag p) :
    unwind (hops p [gitlab, drone, tekton, woodpecker]).1 (hops p [gitlab, drone, tekton, woodpecker]).2 = p := by
  apply chain_roundtrip
  apply chainFrag_run adapters adapters_closed _ _ p ((chainFrag_adapters_iff p).2 h)
  intro a ha
  simp only [List.mem_cons, List.not_mem_nil, or_false] at ha
  simp only [adapters, List.mem_cons, List.not_mem_nil, or_false]
  rcases ha with rfl | rfl | rfl | rfl <;> simp [gitlab, drone, tekton, woodpecker]

/-- The cross round trip with metadata: a pipeline that survives Tekton, then GHA, comes back from GHA. -/
theorem cross_tekton_gha (p : Pipe) (h : RoundTrip.Both p ∧ tekton.Frag p) :
    Gha.fromGha (Gha.toGha (Tekton.fromTekton (Tekton.toTekton p) (Tekton.tektonMeta p))) = p := by
  rw [Tekton.tekton_from_to p h.2]
  exact Gha.gha_from_to p h.1.1

/-- The cross round trip with metadata: a pipeline that survives Tekton, then GitLab, comes back from GitLab. -/
theorem cross_tekton_gitlab (p : Pipe) (h : RoundTrip.Both p ∧ tekton.Frag p) :
    Gitlab.fromGitlab (Gitlab.toGitlab (Tekton.fromTekton (Tekton.toTekton p) (Tekton.tektonMeta p)))
      (Gitlab.gitlabMeta p) = p := by
  rw [Tekton.tekton_from_to p h.2]
  exact Gitlab.gitlab_from_to p h.1.2

end Chain
end Pipemaster
