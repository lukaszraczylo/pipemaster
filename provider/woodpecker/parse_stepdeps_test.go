package woodpecker_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const outOfOrderSteps = `steps:
  - name: deploy
    image: alpine
    depends_on: [build, test]
    commands: [echo deploy]
  - name: test
    image: alpine
    depends_on: [build]
    commands: [echo test]
  - name: build
    image: alpine
    commands: [echo build]
  - name: lint
    image: alpine
    commands: [echo lint]
`

func stepIDs(j *ir.Job) []string {
	var ids []string
	for _, s := range j.Steps {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestStepsFollowDependsOn(t *testing.T) {
	_, j, rep := single(t, outOfOrderSteps)
	want := []string{"build", "test", "deploy", "lint"}
	if got := stepIDs(j); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	found := false
	for _, e := range rep.Entries {
		if e.Level == caps.Emulated && e.Strategy == "steps ordered by depends_on" && strings.Contains(e.Detail, "do not run in parallel") {
			found = true
		}
		if e.Level == caps.Lost && strings.Contains(e.Detail, "depends_on") {
			t.Errorf("depends_on still reported lost: %+v", e)
		}
	}
	if !found {
		t.Errorf("no emulated entry: %+v", rep.Entries)
	}
}

func TestStepsKeepFileOrderWhenDependenciesAllow(t *testing.T) {
	_, j, _ := single(t, "steps:\n  - name: a\n    image: x\n    commands: [one]\n  - name: b\n    image: x\n    depends_on: a\n    commands: [two]\n  - name: c\n    image: x\n    commands: [three]\n")
	if got := stepIDs(j); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("order = %v", got)
	}
	if _, has := ext(j.Steps[0].Ext)["_pos"]; has {
		t.Fatal("an unmoved step must not carry a position")
	}
}

func TestStepDependsOnErrors(t *testing.T) {
	for name, tc := range map[string]struct{ yml, want string }{
		"cycle":   {"steps:\n  - name: a\n    image: x\n    depends_on: [b]\n    commands: [one]\n  - name: b\n    image: x\n    depends_on: [a]\n    commands: [two]\n", "line 4: depends_on forms a cycle"},
		"self":    {"steps:\n  - name: a\n    image: x\n    depends_on: [a]\n    commands: [one]\n", "cycle"},
		"unknown": {"steps:\n  - name: a\n    image: x\n    depends_on: [ghost]\n    commands: [one]\n", `line 4: step a: depends_on names the step "ghost"`},
	} {
		t.Run(name, func(t *testing.T) {
			err := parseErr(t, map[string]string{".woodpecker.yaml": tc.yml})
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestStepDependsOnRoundTripKeepsFileOrder(t *testing.T) {
	p := parseProject(t, "woodpecker", map[string]string{".woodpecker.yaml": outOfOrderSteps})
	files, _ := emitFiles(t, p)
	back := reparse(t, files)
	if want, got := stepIDs(p.Pipelines[0].Jobs[0]), stepIDs(back.Pipelines[0].Jobs[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip order %v, want %v", got, want)
	}
	var body string
	for _, b := range files {
		body = b
	}
	first := strings.Index(body, "name: deploy")
	last := strings.Index(body, "name: build")
	if first < 0 || last < 0 || first > last {
		t.Fatalf("file order not restored:\n%s", body)
	}
	if !strings.Contains(body, "depends_on:") {
		t.Fatalf("depends_on not written:\n%s", body)
	}
}

func TestStepDependsOnToGHAOrdersSteps(t *testing.T) {
	p := parseProject(t, "woodpecker", map[string]string{".woodpecker.yaml": outOfOrderSteps})
	ids := stepIDs(p.Pipelines[0].Jobs[0])
	if ids[0] != "build" {
		t.Fatalf("the IR must hold the dependency first: %v", ids)
	}
}
