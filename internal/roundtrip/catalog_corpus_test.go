package roundtrip_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

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
			"oc patch deployment ${{ inputs.deployment-name }}",
			`origin "${{ inputs.git-revision }}"`,
			`git remote add origin "${{ inputs.git-url }}"`,
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
			"oc apply -f k8s",
			"git submodule update --init --recursive",
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
			for _, bad := range []string{"a task reference has no", `run: "true"`, "$(inputs.params.", "$(params."} {
				if strings.Contains(files, bad) || strings.Contains(rep, bad) {
					t.Errorf("%s output holds the placeholder %q", c.to, bad)
				}
			}
			for _, w := range []string{"needs a privileged runner", "pushing the image needs registry credentials", "the job clones the pipeline repository itself"} {
				if !strings.Contains(rep, w) {
					t.Errorf("report lacks %q", w)
				}
			}
			if !strings.Contains(rep, "mapped to the catalog task buildah") || !strings.Contains(rep, "git clone script") {
				t.Errorf("report lacks the catalog verdicts:\n%s", rep)
			}
		})
	}
}

// block returns the text of one top-level GitLab job, or of one GHA job under jobs:.
func block(t *testing.T, files, head string, indent int) string {
	t.Helper()
	i := strings.Index(files, "\n"+head+":\n")
	if i < 0 {
		t.Fatalf("no job %q in\n%s", head, files)
	}
	rest := files[i+1:]
	pad := strings.Repeat(" ", indent)
	lines := strings.Split(rest, "\n")
	out := []string{lines[0]}
	for _, l := range lines[1:] {
		if l != "" && !strings.HasPrefix(l, pad+" ") && !strings.HasPrefix(l, "#") {
			break
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func TestCorpus69LocalTaskJobsWork(t *testing.T) {
	gha, _ := emitCorpus69(t, provider.GHA)
	gl, _ := emitCorpus69(t, provider.GitLab)
	jobs := map[string]string{
		"gha apply-manifests":    block(t, gha, "  apply-manifests", 2),
		"gha update-deployment":  block(t, gha, "  update-deployment", 2),
		"gitlab apply-manifests": block(t, gl, "build-and-deploy:apply-manifests", 0),
		"gitlab update":          block(t, gl, "build-and-deploy:update-deployment", 0),
	}
	for name, b := range jobs {
		for _, bad := range []string{"/workspace/", "pipemaster:lost", `"true"`, "$(params.", "$(inputs."} {
			if strings.Contains(b, bad) {
				t.Errorf("%s holds %q:\n%s", name, bad, b)
			}
		}
		if !strings.Contains(b, "oc apply -f k8s") && !strings.Contains(b, "oc patch deployment") {
			t.Errorf("%s lacks its command:\n%s", name, b)
		}
	}
	items := commandItems(t, emitCorpus69Files(t, provider.GitLab))
	assertOneCommandItem(t, items, "Applying manifests in k8s directory")
	assertOneCommandItem(t, items, "oc patch deployment")
	if !strings.Contains(jobs["gha apply-manifests"], "uses: actions/checkout@") {
		t.Error("the GHA apply-manifests job needs its own checkout")
	}
	if !strings.Contains(jobs["gitlab update"], "$deployment_name") || !strings.Contains(jobs["gha update-deployment"], "${{ inputs.deployment-name }}") {
		t.Error("update-deployment must read the hyphenated input in both targets")
	}
}

// emitCorpus69Files converts corpus entry 69 and returns the emitted files by name.
func emitCorpus69Files(t *testing.T, to string) map[string][]byte {
	t.Helper()
	proj, _, err := prov(t, provider.Tekton).Parse(os.DirFS(catalogCorpus))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := prov(t, to).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// commandItems returns every string item of a script or commands list in the emitted files.
func commandItems(t *testing.T, files map[string][]byte) []string {
	t.Helper()
	var items []string
	var walk func(n any)
	walk = func(n any) {
		switch v := n.(type) {
		case map[string]any:
			for k, c := range v {
				if l, ok := c.([]any); ok && (k == "script" || k == "commands") {
					for _, it := range l {
						if s, ok := it.(string); ok {
							items = append(items, s)
						}
					}
				}
				walk(c)
			}
		case []any:
			for _, c := range v {
				walk(c)
			}
		}
	}
	for name, data := range files {
		dec := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var doc any
			if err := dec.Decode(&doc); err != nil {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("%s is not valid YAML: %v", name, err)
				}
				break
			}
			walk(doc)
		}
	}
	return items
}

// assertOneCommandItem requires that the item holding marker is one command that bash accepts whole: an item
// that a runner executes by itself cannot leave a quote open for the next item.
func assertOneCommandItem(t *testing.T, items []string, marker string) string {
	t.Helper()
	var found []string
	for _, it := range items {
		if strings.Contains(it, marker) {
			found = append(found, it)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%q is in %d script items, want 1:\n%q", marker, len(found), items)
	}
	syntax := exec.Command("bash", "-n")
	syntax.Stdin = strings.NewReader(found[0])
	if out, err := syntax.CombinedOutput(); err != nil {
		t.Errorf("the item holding %q is not valid shell on its own: %v\n%s\n%s", marker, err, out, found[0])
	}
	if !strings.Contains(found[0], "bash -c") {
		t.Errorf("the item holding %q lost its bash -c command:\n%s", marker, found[0])
	}
	return found[0]
}

func TestCorpus69BashCommandIsOneBalancedItem(t *testing.T) {
	for _, to := range []string{provider.GitLab, provider.Drone, provider.Woodpecker} {
		t.Run(to, func(t *testing.T) {
			items := commandItems(t, emitCorpus69Files(t, to))
			assertOneCommandItem(t, items, "Applying manifests in k8s directory")
			if to != provider.GitLab {
				// Drone and Woodpecker have no pipeline inputs: the step that reads one is reported lost.
				return
			}
			it := assertOneCommandItem(t, items, "oc patch deployment")
			if !strings.Contains(it, "$deployment_name") || strings.Contains(it, "inputs.") {
				t.Errorf("the input reference is not translated:\n%s", it)
			}
		})
	}
}

func TestCorpus69GitLabReturnsTheInputReference(t *testing.T) {
	out := emitCorpus69Files(t, provider.GitLab)
	fs := fstest.MapFS{}
	for n, d := range out {
		fs[n] = &fstest.MapFile{Data: d}
	}
	proj, rep, err := prov(t, provider.GitLab).Parse(fs)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "marker ignored") {
			t.Errorf("a marker was ignored: %s %s", e.IRPath, e.Detail)
		}
	}
	back, _, err := prov(t, provider.Tekton).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, d := range back {
		all.Write(d)
	}
	if !strings.Contains(all.String(), "$(params.deployment-name)") || strings.Contains(all.String(), "$deployment_name") {
		t.Errorf("the return trip lost the input reference:\n%s", all.String())
	}
}

func TestCorpus69TektonReportNamesNoUnwrittenCheckout(t *testing.T) {
	files, rep := emitCorpus69(t, provider.Tekton)
	if strings.Contains(files, "checkout") {
		t.Errorf("the Tekton files name a checkout step:\n%s", files)
	}
	for _, l := range strings.Split(rep, "\n") {
		if strings.Contains(l, "-checkout") || strings.Contains(l, "checkout step in the job") ||
			(strings.Contains(l, "/step/") && strings.Contains(l, "step.action")) {
			t.Errorf("an entry names a step or checkout that the Tekton files do not write: %s", l)
		}
	}
}
