package provider

import "testing"

func TestFamily(t *testing.T) {
	cases := map[string]string{
		GHA: GHA, Gitea: GHA, Forgejo: GHA,
		GitLab: GitLab, Drone: Drone, Woodpecker: Woodpecker,
		Concourse: Concourse, Tekton: Tekton, Argo: Argo,
	}
	for id, want := range cases {
		if got := Family(id); got != want {
			t.Errorf("Family(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestSameFamily(t *testing.T) {
	if !SameFamily(Gitea, Forgejo) || !SameFamily(GHA, Gitea) {
		t.Error("gha, gitea and forgejo must share a family")
	}
	if SameFamily(GHA, GitLab) {
		t.Error("gha and gitlab must not share a family")
	}
}
