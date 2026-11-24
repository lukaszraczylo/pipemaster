//go:build behave

package behave

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// Engine runs one provider's pipeline files in a prepared workspace for one event.
type Engine interface {
	Run(ctx context.Context, w *Workspace) (*Trace, error)
}

// Engine timeouts: a run past its cap counts as a failed run, not a hang.
const (
	timeoutLocal = 5 * time.Minute
	timeoutK8s   = 6 * time.Minute
)

const (
	provGHA        = "gha"
	provGitLab     = "gitlab"
	provWoodpecker = "woodpecker"
	provDrone      = "drone"
	provTekton     = "tekton"
	provArgo       = "argo"
	provConcourse  = "concourse"
)

// Targets are the providers a converted pipeline runs on.
var Targets = []string{provGHA, provGitLab, provWoodpecker, provDrone, provTekton, provArgo, provConcourse}

var markRe = regexp.MustCompile(`^MARK (.*)$`)

// markers extracts the MARK payloads from engine-stripped stdout lines.
func markers(lines []string) []string {
	var out []string
	for _, l := range lines {
		if m := markRe.FindStringSubmatch(strings.TrimRight(l, "\r ")); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// payloadLines keeps the capture group of re for every matching line. Command echoes do not match.
func payloadLines(text string, re *regexp.Regexp) []string {
	var out []string
	for _, l := range strings.Split(stripANSI(text), "\n") {
		if m := re.FindStringSubmatch(strings.TrimRight(l, "\r")); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// EngineFor returns the engine that runs a provider's files.
func (e *Env) EngineFor(provider string, k *Kube, c *Concourse) Engine {
	switch provider {
	case provGHA:
		return &actEngine{env: e}
	case provGitLab:
		return &gitlabEngine{env: e}
	case provWoodpecker:
		return &woodpeckerEngine{env: e}
	case provDrone:
		return &droneEngine{env: e}
	case provTekton:
		return &tektonEngine{env: e, kube: k}
	case provArgo:
		return &argoEngine{env: e, kube: k}
	case provConcourse:
		return &concourseEngine{env: e, conc: c}
	}
	return nil
}
