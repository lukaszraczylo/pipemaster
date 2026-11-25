//go:build behave

package behave

import (
	"os"
	"path/filepath"
	"slices"

	"go.yaml.in/yaml/v3"
)

// Requirements a fixture lists in its requires field.
const (
	reqArtifacts    = "artifacts"
	reqAllowFailure = "allow_failure"
)

// engineCap states what one engine cannot do on this setup, each with the evidence for the claim.
type engineCap struct {
	unsupported map[string]string
	eventWhy    string
	events      []string
	noSHA       bool
}

// capsOf returns the capability declaration of a provider's engine.
func capsOf(provider string) engineCap {
	switch provider {
	case provDrone:
		return engineCap{noSHA: true}
	case provConcourse:
		return engineCap{events: []string{EvPushMain, EvPushBranch, EvTag}, eventWhy: "Concourse has no pull request event: the converted file has no pull request resource, so no resource version stands for it"}
	}
	return engineCap{}
}

// limit returns the reason a pair cannot be judged on the target engine, or an empty string.
func limit(fx Fixture, target, event string) string {
	c := capsOf(target)
	if c.events != nil && !slices.Contains(c.events, event) {
		return c.eventWhy
	}
	for _, req := range fx.Requires {
		if why, ok := c.unsupported[req]; ok {
			return why
		}
	}
	return ""
}

const jobContinueWhy = "act does not count a failed job with continue-on-error as a success for the jobs that need it"

// actJobContinue reports a workflow in dir where a job with continue-on-error has a dependent job.
func actJobContinue(dir string) string {
	files, _ := filepath.Glob(filepath.Join(dir, ".github", "workflows", "*.y*ml"))
	for _, f := range files {
		raw, err := os.ReadFile(f) // #nosec G304 G122 -- a workflow of the harness workspace
		if err != nil {
			continue
		}
		var doc struct {
			Jobs map[string]struct {
				Needs           any  `yaml:"needs"`
				ContinueOnError bool `yaml:"continue-on-error"`
			} `yaml:"jobs"`
		}
		if yaml.Unmarshal(raw, &doc) != nil {
			continue
		}
		for id, j := range doc.Jobs {
			if !j.ContinueOnError {
				continue
			}
			for _, o := range doc.Jobs {
				if needsJob(o.Needs, id) {
					return jobContinueWhy
				}
			}
		}
	}
	return ""
}

func needsJob(needs any, id string) bool {
	switch n := needs.(type) {
	case string:
		return n == id
	case []any:
		return slices.Contains(n, any(id))
	}
	return false
}
