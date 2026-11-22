package argo_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	containersHead = "        containers:\n"
	containersTail = "        volumeMounts:\n"
	blockStart     = "          - name: "
)

func frobJob() *ir.Job {
	return &ir.Job{ID: "build", Steps: []*ir.Step{
		{ID: "one", Kind: ir.StepScript, Script: []string{"echo one"}},
		actionStep("frob", "acme", "frobnicate", nil),
		{ID: "two", Kind: ir.StepScript, Script: []string{"echo two"}},
		{ID: "three", Kind: ir.StepScript, Script: []string{"echo three"}},
	}}
}

// containerEdit rewrites the container list of the emitted file: the blocks come without their dependencies,
// and edit may remove, add or change them. The result is chained again, so the file stays one chain.
type containerEdit struct {
	blocks map[string]string
	order  []string
}

func scriptBlock(name, line string) string {
	return blockStart + name + "\n            image: alpine:3.22\n            command:\n              - sh\n              - -ec\n            args:\n              - " + line + "\n            workingDir: /workspace\n"
}

func splitContainers(t *testing.T, src string) (head string, ce *containerEdit, tail string) {
	t.Helper()
	i, j := strings.Index(src, containersHead), strings.Index(src, containersTail)
	if i < 0 || j < i {
		t.Fatalf("no container list in:\n%s", src)
	}
	i += len(containersHead)
	ce = &containerEdit{blocks: map[string]string{}}
	for _, chunk := range strings.Split(src[i:j], blockStart)[1:] {
		name, body, _ := strings.Cut(chunk, "\n")
		var kept []string
		skip := false
		for _, l := range strings.Split(body, "\n") {
			switch {
			case strings.HasPrefix(l, "            dependencies:"):
				skip = true
			case skip && strings.HasPrefix(l, "              - "):
			default:
				skip = false
				kept = append(kept, l)
			}
		}
		ce.order = append(ce.order, name)
		ce.blocks[name] = blockStart + name + "\n" + strings.TrimRight(strings.Join(kept, "\n"), "\n") + "\n"
	}
	return src[:i], ce, src[j:]
}

func (ce *containerEdit) text() string {
	var b strings.Builder
	for i, n := range ce.order {
		b.WriteString(ce.blocks[n])
		if i > 0 {
			b.WriteString("            dependencies:\n              - " + ce.order[i-1] + "\n")
		}
	}
	return b.String()
}

func (ce *containerEdit) remove(name string) {
	ce.order = slices.DeleteFunc(ce.order, func(n string) bool { return n == name })
	delete(ce.blocks, name)
}

func (ce *containerEdit) insertBefore(t *testing.T, at, name, line string) {
	t.Helper()
	i := slices.Index(ce.order, at)
	if i < 0 {
		t.Fatalf("the file has no container %s: %v", at, ce.order)
	}
	ce.order = slices.Insert(ce.order, i, name)
	ce.blocks[name] = scriptBlock(name, line)
}

func frobParse(t *testing.T, edit func(*testing.T, *containerEdit)) ([]string, *ir.Job, string) {
	t.Helper()
	files, _ := emit(t, project(frobJob()))
	src := files[ciFile]
	if edit != nil {
		head, ce, tail := splitContainers(t, src)
		edit(t, ce)
		src = head + ce.text() + tail
		files[ciFile] = src
	}
	p, rep, err := parseRepo(files)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	j := p.Pipelines[0].Jobs[0]
	var ids []string
	for _, st := range j.Steps {
		id := st.ID
		if st.Kind == ir.StepAction {
			id += "!"
		}
		ids = append(ids, id)
	}
	return ids, j, joinEntries(rep)
}

func joinEntries(rep *report.Report) string {
	var out []string
	for _, e := range rep.Entries {
		out = append(out, e.IRPath+": "+e.Detail)
	}
	return strings.Join(out, "\n")
}

func wantIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("steps = %v, want %v", got, want)
	}
}

func TestHeldUnmappedActionKeepsItsPlace(t *testing.T) {
	ids, _, rep := frobParse(t, nil)
	wantIDs(t, ids, "one", "frob!", "two", "three")
	if strings.Contains(rep, "marker ignored") {
		t.Errorf("unexpected marker ignored entry:\n%s", rep)
	}
}

func TestRemovedPlaceholderDoesNotRestoreTheAction(t *testing.T) {
	ids, _, rep := frobParse(t, func(_ *testing.T, ce *containerEdit) { ce.remove("frob") })
	wantIDs(t, ids, "one", "two", "three")
	if !strings.Contains(rep, "marker ignored: the placeholder container was removed") {
		t.Errorf("no entry for the removed placeholder:\n%s", rep)
	}
}

func TestEditsAroundThePlaceholderLeaveTheActionNextToIt(t *testing.T) {
	cases := []struct {
		name string
		edit func(*testing.T, *containerEdit)
		want []string
	}{
		{"delete the step before", func(_ *testing.T, ce *containerEdit) { ce.remove("one") }, []string{"frob!", "two", "three"}},
		{"insert a step before the previous one", func(t *testing.T, ce *containerEdit) { ce.insertBefore(t, "one", "new", "echo new") }, []string{"new", "one", "frob!", "two", "three"}},
		{"insert a step before the action", func(t *testing.T, ce *containerEdit) { ce.insertBefore(t, "frob", "mid", "echo mid") }, []string{"one", "mid", "frob!", "two", "three"}},
		{"delete the later steps", func(_ *testing.T, ce *containerEdit) { ce.remove("two"); ce.remove("three") }, []string{"one", "frob!"}},
		{"delete all other steps", func(_ *testing.T, ce *containerEdit) { ce.remove("one"); ce.remove("two"); ce.remove("three") }, []string{"frob!"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ids, _, rep := frobParse(t, c.edit)
			wantIDs(t, ids, c.want...)
			if strings.Contains(rep, "marker ignored") || strings.Contains(rep, "repeats in the job") {
				t.Errorf("unexpected entry:\n%s", rep)
			}
		})
	}
}

func TestEditedPlaceholderKeepsTheFile(t *testing.T) {
	ids, j, rep := frobParse(t, func(_ *testing.T, ce *containerEdit) {
		ce.blocks["frob"] = strings.Replace(ce.blocks["frob"], `- "true"`, "- echo hi", 1)
	})
	wantIDs(t, ids, "one", "frob", "two", "three")
	if st := stepByID(j, "frob"); st == nil || !slices.Equal(st.Script, []string{"echo hi"}) {
		t.Errorf("frob step = %+v, want the edited script", st)
	}
	if !strings.Contains(rep, "marker ignored: the placeholder container was edited") {
		t.Errorf("no entry for the edited placeholder:\n%s", rep)
	}
}

func TestPlaceholderImageEditKeepsTheFile(t *testing.T) {
	ids, _, rep := frobParse(t, func(_ *testing.T, ce *containerEdit) {
		ce.blocks["frob"] = strings.Replace(ce.blocks["frob"], "alpine:3.22", "busybox:1", 1)
	})
	wantIDs(t, ids, "one", "frob", "two", "three")
	if !strings.Contains(rep, "marker ignored") {
		t.Errorf("no marker ignored entry:\n%s", rep)
	}
}

func TestHeldDownloadAndOnlyStepJobKeepPlace(t *testing.T) {
	j := &ir.Job{ID: "build", Outputs: []ir.Output{{Name: "v", Value: "1"}}, Steps: []*ir.Step{actionStep("frob", "acme", "frobnicate", nil)}}
	files, _ := emit(t, project(j))
	p, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Pipelines[0].Jobs[0].Steps; len(got) != 1 || got[0].ID != "frob" || got[0].Kind != ir.StepAction {
		t.Errorf("steps = %+v, want only the held action", got)
	}
	if !strings.Contains(files[ciFile], "name: main") {
		t.Errorf("a job with outputs needs a container named main:\n%s", files[ciFile])
	}
}

func TestPlaceholderPassesShAndArgoLint(t *testing.T) {
	files, _ := emit(t, project(frobJob()))
	if out, err := exec.Command("sh", "-n", "-c", "true").CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v %s", err, out)
	}
	if !strings.Contains(files[ciFile], "- \"true\"") {
		t.Fatalf("the placeholder does not run true:\n%s", files[ciFile])
	}
	linttool.RequireArgo(t)
	if out, err := linttool.ArgoLint(t, ciFile, []byte(files[ciFile])); err != nil {
		t.Fatalf("argo lint: %v\n%s", err, out)
	}
}
