package roundtrip_test

import (
	"os"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider"
)

const catalogCorpus = corpusRoot + "/69-tekton-pipelines-tutorial"

// emitCorpus69 converts corpus entry 69 and returns the text of every emitted file and of its report.
func emitCorpus69(t *testing.T, to string) (files string, report string) {
	t.Helper()
	proj, rep, err := prov(t, provider.Tekton).Parse(os.DirFS(catalogCorpus))
	if err != nil {
		t.Fatal(err)
	}
	out, emitRep, err := prov(t, to).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	rep.Merge(emitRep)
	var fb, rb strings.Builder
	for name, data := range out {
		fb.WriteString("# file " + name + "\n" + string(data) + "\n")
	}
	for _, e := range rep.Entries {
		rb.WriteString(e.IRPath + " " + string(e.Feature) + " " + e.Strategy + " " + e.Detail + "\n")
	}
	return fb.String(), rb.String()
}

func TestCorpus69CatalogTasksHaveNoPlaceholder(t *testing.T) {
	cases := []struct {
		to   string
		want []string
	}{
		{provider.GHA, []string{
			"uses: actions/checkout@",
			"id: build-image-checkout",
			"id: yaml-lint-checkout",
			"oc patch deployment $(inputs.params.deployment)",
			"ref: ${{ inputs.git-revision }}",
			"container: quay.io/buildah/stable:v1",
			"PARAM_IMAGE: ${{ inputs.IMAGE }}",
			`buildah "--storage-driver=${PARAM_STORAGE_DRIVER}" bud`,
			"uses: docker://image-registry.openshift-image-registry.svc:5000/openshift/cli:latest",
			"oc patch deployment",
			"container: docker.io/cytopia/yamllint:1.23@sha256:",
			"yamllint .",
		}},
		{provider.GitLab, []string{
			"image: quay.io/buildah/stable:v1",
			"GIT_SUBMODULE_STRATEGY: normal",
			"buildah ",
			"oc patch deployment",
			"yamllint .",
		}},
	}
	for _, c := range cases {
		t.Run(c.to, func(t *testing.T) {
			files, rep := emitCorpus69(t, c.to)
			for _, w := range c.want {
				if !strings.Contains(files, w) {
					t.Errorf("%s output lacks %q\n%s", c.to, w, files)
				}
			}
			for _, bad := range []string{"a task reference has no", `run: "true"`} {
				if strings.Contains(files, bad) || strings.Contains(rep, bad) {
					t.Errorf("%s output holds the placeholder %q", c.to, bad)
				}
			}
			for _, w := range []string{"needs a privileged runner", "pushing the image needs registry credentials", "the job clones the pipeline repository itself", "$(inputs.params.deployment) is a Tekton variable with no IR reference"} {
				if !strings.Contains(rep, w) {
					t.Errorf("report lacks %q", w)
				}
			}
			if !strings.Contains(rep, "mapped to the catalog task buildah") || !strings.Contains(rep, "param URL of the catalog task git-clone has no checkout input") {
				t.Errorf("report lacks the catalog verdicts:\n%s", rep)
			}
		})
	}
}
