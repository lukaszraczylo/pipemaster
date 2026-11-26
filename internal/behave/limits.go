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
	case provArgo:
		return engineCap{unsupported: map[string]string{reqArtifacts: argoArtifactsWhy}}
	case provConcourse:
		return engineCap{events: []string{EvPushMain, EvPushBranch, EvTag}, eventWhy: concoursePRWhy}
	}
	return engineCap{}
}

// eventUnsupported is true when the engine of the target has no way to start a run for the event.
func eventUnsupported(target, event string) bool {
	c := capsOf(target)
	return c.events != nil && !slices.Contains(c.events, event)
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

// concoursePRWhy cites the git resource: its check returns the commits of one branch or the tags that match
// tag_filter, so a pull request has no version unless the file declares a pull request resource
// (https://github.com/concourse/git-resource/blob/v1.9.1/README.md, "check: Check for new commits"). The converted file
// declares none, because a pull request resource is a separate resource type (github-pr-resource).
const concoursePRWhy = "Concourse has no pull request event: the converted file has no pull request resource, so no resource version stands for it " +
	"(https://github.com/concourse/git-resource/blob/v1.9.1/README.md, check: the versions are commits of one branch or tags)"

// argoArtifactsWhy cites the Argo Workflows rule: "To run Argo workflows that use artifacts, you must configure
// and use an artifact repository." (https://argo-workflows.readthedocs.io/en/latest/configure-artifact-repository/).
// The Kiri cluster of the harness holds none, and the converted file names none.
const argoArtifactsWhy = "Argo needs an artifact repository for artifacts, and the harness cluster has none " +
	"(https://argo-workflows.readthedocs.io/en/latest/configure-artifact-repository/)"

// jobContinueWhy cites the act source: setJobResult in pkg/runner/job_executor.go of nektos/act v0.2.90 sets the
// job result to failure from the job error alone and never reads the job-level continue-on-error, so the jobs that
// need the job are skipped. GitHub counts such a job as a success. A run of act 0.2.90 on the allow_failure
// fixture skipped the dependent job.
const jobContinueWhy = "act 0.2.90 does not count a failed job with continue-on-error as a success for the jobs that need it " +
	"(nektos/act v0.2.90 pkg/runner/job_executor.go setJobResult reads the job error only, not continue-on-error)"

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
