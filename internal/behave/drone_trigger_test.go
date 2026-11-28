//go:build behave

package behave

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

const droneTriggerFile = `kind: pipeline
name: off
trigger:
  event:
    exclude:
      - '**'
steps: []
---
kind: pipeline
name: tags
trigger:
  ref:
    - refs/tags/**
steps: []
---
kind: pipeline
name: main
trigger:
  event: [push, tag]
  ref: [refs/heads/main]
steps: []
---
kind: pipeline
name: always
steps: []
`

func TestDronePipelinesFollowTheirTrigger(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".drone.yml")
	if err := os.WriteFile(file, []byte(droneTriggerFile), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		run  droneRun
		want []string
	}{
		"push of main":   {droneRun{"push", "main", "refs/heads/main"}, []string{"main", "always"}},
		"push of branch": {droneRun{"push", "feat", "refs/heads/feat"}, []string{"always"}},
		"tag":            {droneRun{"tag", "", "refs/tags/v1"}, []string{"tags", "always"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, _, err := dronePipelines(file, tc.run)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("pipelines = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestDroneTriggerRefusesWhatItCannotEvaluate(t *testing.T) {
	r := droneRun{"push", "main", "refs/heads/main"}
	if _, err := r.triggerMatches(map[string]any{"repo": "a/b"}); err == nil {
		t.Fatal("an unknown trigger key must be an error")
	}
	if _, err := r.triggerMatches(map[string]any{"ref": []any{"refs/heads/{main,dev}"}}); err == nil {
		t.Fatal("an unsupported glob must be an error")
	}
}

func TestDroneStatusCondition(t *testing.T) {
	for name, tc := range map[string]struct {
		trigger         map[string]any
		success, failed bool
	}{
		"absent is success only": {map[string]any{}, true, false},
		"both":                   {map[string]any{"status": []any{"success", "failure"}}, true, true},
		"failure only":           {map[string]any{"status": "failure"}, false, true},
		"exclude failure":        {map[string]any{"status": map[string]any{"exclude": []any{"failure"}}}, true, false},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := statusOf(tc.trigger)
			if err != nil {
				t.Fatal(err)
			}
			if s.admits(statusSuccess) != tc.success || s.admits(statusFailure) != tc.failed {
				t.Fatalf("admits success=%v failure=%v", s.admits(statusSuccess), s.admits(statusFailure))
			}
		})
	}
	if _, err := statusOf(map[string]any{"status": "skipped"}); err == nil {
		t.Fatal("an unknown status must be an error")
	}
}

func TestWoodpeckerWorkflowStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		doc             string
		success, failed bool
		bad             bool
	}{
		"no when":            {doc: "steps: []\n", success: true},
		"entry without":      {doc: "when:\n  event: push\n", success: true},
		"failure in entries": {doc: "when:\n  - event: push\n    status: [failure]\n  - event: tag\n    status: failure\n", failed: true},
		"both":               {doc: "when:\n  status: [success, failure]\n", success: true, failed: true},
		"mixed lists":        {doc: "when:\n  - event: push\n    status: [failure]\n  - event: tag\n", bad: true},
	} {
		t.Run(name, func(t *testing.T) {
			var d struct {
				When yaml.Node `yaml:"when"`
			}
			if err := yaml.Unmarshal([]byte(tc.doc), &d); err != nil {
				t.Fatal(err)
			}
			s, err := wpStatus(&d.When)
			if (err != nil) != tc.bad {
				t.Fatalf("err = %v, want error %v", err, tc.bad)
			}
			if !tc.bad && (s.admits(statusSuccess) != tc.success || s.admits(statusFailure) != tc.failed) {
				t.Fatalf("admits success=%v failure=%v", s.admits(statusSuccess), s.admits(statusFailure))
			}
		})
	}
}
