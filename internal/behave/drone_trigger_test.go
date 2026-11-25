//go:build behave

package behave

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
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
			got, err := dronePipelines(file, tc.run)
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
