package gha

import (
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	cachePolicyPull = "pull"
	cachePolicyPush = "push"
	monthDays       = 30
	weekDays        = 7
	hoursPerDay     = 24
)

var expireRE = regexp.MustCompile(`^(\d+)\s*(hours?|days?|weeks?|months?)$`)

// reverseAction returns the pinned action for a construct GHA has no key for.
func reverseAction(kind, sub string) (*ir.ActionRef, error) {
	ref, ok := actions.Reverse(kind)
	if !ok {
		return nil, errNoReverse(kind)
	}
	ref.Path = sub
	return &ref, nil
}

type errNoReverse string

func (k errNoReverse) Error() string { return "actions: no pinned action for " + string(k) }

func marked(m *omap, marker string) *yaml.Node {
	m.n.HeadComment = marker
	return m.n
}

func pathNode(paths []string) *yaml.Node { return block(paths) }

func (e *emitter) templateNode(path, s string) (*yaml.Node, error) {
	t, err := e.template(path, s)
	return str(t), err
}

// preSteps are the steps that run before the job's own: artifact downloads, then the cache restore.
func (e *emitter) preSteps(c jobCtx, jp string, j *ir.Job) ([]*yaml.Node, error) {
	var out []*yaml.Node
	for _, n := range j.Needs {
		if !n.Artifacts {
			continue
		}
		names := e.producerArtifacts(c.pl, n.JobID)
		if len(names) == 0 {
			names = []string{""}
		}
		for _, name := range names {
			ref, err := reverseAction("download", "")
			if err != nil {
				return nil, err
			}
			m := newMap()
			m.set("uses", str(actionString(ref)))
			if name != "" {
				m.set("with", withOf("name", str(name)))
			}
			out = append(out, marked(m, emitkit.Marker(caps.JobNeedsArtifacts, jp)))
		}
	}
	for _, ch := range j.Cache {
		n, err := e.cacheStep(jp, j, ch)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func withOf(kv ...any) *yaml.Node {
	m := newMap()
	for i := 0; i+1 < len(kv); i += 2 {
		m.set(kv[i].(string), kv[i+1].(*yaml.Node))
	}
	return m.n
}

func (e *emitter) cacheStep(jp string, j *ir.Job, ch ir.Cache) (*yaml.Node, error) {
	sub := ""
	switch ch.Policy {
	case cachePolicyPull:
		sub = "restore"
	case cachePolicyPush:
		sub = "save"
	case "", "pull-push":
	default:
		e.emulate(jp, caps.JobCache, "actions/cache step", "cache policy "+ch.Policy+" is unknown; the step restores and saves")
	}
	ref, err := reverseAction("cache", sub)
	if err != nil {
		return nil, err
	}
	key := ch.Key
	if key == "" {
		key = j.ID + "-cache"
	}
	kn, err := e.templateNode(jp, key)
	if err != nil {
		return nil, err
	}
	m := newMap()
	m.set("uses", str(actionString(ref)))
	m.set("with", withOf("path", pathNode(ch.Paths), "key", kn))
	return marked(m, emitkit.Marker(caps.JobCache, jp)), nil
}

func artifactName(j *ir.Job, i int, a ir.Artifact) string {
	switch {
	case a.Name != "":
		return a.Name
	case i == 0:
		return j.ID
	}
	return j.ID + "-" + strconv.Itoa(i+1)
}

// producerArtifacts names the artifacts of job id, so a consumer downloads exactly those.
func (e *emitter) producerArtifacts(pl *ir.Pipeline, id string) []string {
	for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
		if j != nil && j.ID == id {
			names := make([]string, len(j.Artifacts))
			for i, a := range j.Artifacts {
				names[i] = artifactName(j, i, a)
			}
			return names
		}
	}
	return nil
}

// postSteps are the upload steps that close the job.
func (e *emitter) postSteps(jp string, j *ir.Job) ([]*yaml.Node, error) {
	var out []*yaml.Node
	for i, a := range j.Artifacts {
		ref, err := reverseAction("upload", "")
		if err != nil {
			return nil, err
		}
		with := newMap()
		with.set("name", str(artifactName(j, i, a)))
		with.set("path", pathNode(a.Paths))
		if a.Expire != "" {
			if days, ok := retentionDays(a.Expire); ok {
				with.set("retention-days", intNode(days))
			} else {
				e.emulate(jp, caps.JobArtifacts, "upload-artifact step at job end", "expiry "+a.Expire+" is not a day, week or month count; the default retention applies")
			}
		}
		m := newMap()
		m.set("uses", str(actionString(ref)))
		m.set("with", with.n)
		out = append(out, marked(m, emitkit.Marker(caps.JobArtifacts, jp)))
	}
	return out, nil
}

// retentionDays converts "N hours|days|weeks|months" to whole days, rounding hours up.
func retentionDays(s string) (int, bool) {
	m := expireRE.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	switch {
	case strings.HasPrefix(m[2], "hour"):
		return (n + hoursPerDay - 1) / hoursPerDay, true
	case strings.HasPrefix(m[2], "week"):
		return n * weekDays, true
	case strings.HasPrefix(m[2], "month"):
		return n * monthDays, true
	}
	return n, true
}
