package gha

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprconcourse "github.com/lukaszraczylo/pipemaster/expr/concourse"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
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

// synthUses renders the uses: text of an action the emitter adds, which the source never named. It is keyed
// under the job path so the dialect's host note has its own report entry.
func (e *emitter) synthUses(jp, kind string, ref *ir.ActionRef) (string, error) {
	text, _, err := e.uses(jp+"/synth/"+kind, ref)
	return text, err
}

func marked(m *omap, marker string) *yaml.Node {
	m.n.HeadComment = marker
	return m.n
}

func pathNode(paths []string) *yaml.Node { return block(paths) }

func (e *emitter) templateNode(path, s string) (*yaml.Node, error) {
	t, err := e.template(path, s)
	return str(t), err
}

// preSteps are the steps that run before the job's own: the checkout, artifact downloads, then the cache restore.
func (e *emitter) preSteps(c jobCtx, jp string, j *ir.Job) ([]*yaml.Node, error) {
	var out []*yaml.Node
	if n, err := e.checkoutStep(c, jp, j); err != nil {
		return nil, err
	} else if n != nil {
		out = append(out, n)
	}
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
			uses, err := e.synthUses(jp, "download", ref)
			if err != nil {
				return nil, err
			}
			m.set("uses", str(uses))
			if name != "" {
				nn, err := e.templateNode(jp, name)
				if err != nil {
					return nil, err
				}
				m.set("with", withOf("name", nn))
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
	uses, err := e.synthUses(jp, "cache", ref)
	if err != nil {
		return nil, err
	}
	m.set("uses", str(uses))
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
		nn, err := e.templateNode(jp, artifactName(j, i, a))
		if err != nil {
			return nil, err
		}
		with.set("name", nn)
		with.set("path", pathNode(a.Paths))
		if a.Expire != "" {
			if days, ok := retentionDays(a.Expire); ok {
				with.set("retention-days", intNode(days))
			} else {
				e.emulate(jp, caps.JobArtifacts, "upload-artifact step at job end", "expiry "+a.Expire+" is not a day, week or month count; the default retention applies")
			}
		}
		m := newMap()
		uses, err := e.synthUses(jp, "upload", ref)
		if err != nil {
			return nil, err
		}
		m.set("uses", str(uses))
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

// carried is the marker argument for the steps that stand in for cache, artifacts and artifact needs: how many
// steps open and close the job, and the IR values they came from.
type carried struct {
	Cache     []ir.Cache    `json:"cache,omitempty"`
	Artifacts []ir.Artifact `json:"artifacts,omitempty"`
	Needs     []string      `json:"needs,omitempty"`
	Pre       int           `json:"pre"`
	Post      int           `json:"post"`
}

func (e *emitter) markCarried(jp string, j *ir.Job, pre, post int) error {
	if pre+post == 0 {
		return nil
	}
	c := carried{Cache: j.Cache, Artifacts: j.Artifacts, Pre: pre, Post: post}
	for _, n := range j.Needs {
		if n.Artifacts {
			c.Needs = append(c.Needs, n.JobID)
		}
	}
	f := caps.JobArtifacts
	switch {
	case len(c.Needs) > 0:
		f = caps.JobNeedsArtifacts
	case len(c.Cache) > 0:
		f = caps.JobCache
	}
	return e.markArg(f, jp, c)
}

const (
	gitStrategyVar   = "GIT_STRATEGY"
	gitStrategyNone  = "none"
	gitDepthVar      = "GIT_DEPTH"
	gitSubmodulesVar = "GIT_SUBMODULE_STRATEGY"
	submodulesNormal = "normal"
	submodulesDeep   = "recursive"
)

// envLit is the literal value of a variable the job or its pipeline sets; the job wins.
func envLit(c jobCtx, j *ir.Job, name string) string {
	for _, vars := range [][]ir.Var{j.Env, c.pl.Env} {
		for _, v := range vars {
			if v.Name == name && v.Value.Kind == ir.ValueLit {
				return v.Value.Lit
			}
		}
	}
	return ""
}

// checkoutStep is the clone GitLab runs before every job, which a GHA job must ask for. It is nil for any other
// origin, for a job with no steps and for GIT_STRATEGY none. The step is counted as a pre step, so Parse removes it again.
func (e *emitter) checkoutStep(c jobCtx, jp string, j *ir.Job) (*yaml.Node, error) {
	if depth, ok := sourceCheckout(e.from, j); ok {
		return e.sourceCheckoutNode(jp, depth)
	}
	if e.from == nil || e.from.ID != exprgitlab.Dialect || len(j.Steps) == 0 || envLit(c, j, gitStrategyVar) == gitStrategyNone {
		return nil, nil
	}
	ref, err := reverseAction("checkout", "")
	if err != nil {
		return nil, err
	}
	m := newMap()
	uses, err := e.synthUses(jp, "checkout", ref)
	if err != nil {
		return nil, err
	}
	m.set("uses", str(uses))
	with := newMap()
	if d, err := strconv.Atoi(envLit(c, j, gitDepthVar)); err == nil {
		with.set("fetch-depth", intNode(d))
	}
	switch envLit(c, j, gitSubmodulesVar) {
	case submodulesNormal:
		with.set("submodules", boolNodeOf(true))
	case submodulesDeep:
		with.set("submodules", str(submodulesDeep))
	}
	if len(with.n.Content) > 0 {
		m.set("with", with.n)
	}
	e.emulate(jp, caps.StepAction, "actions/checkout step", "GitLab clones the repository before every job and GHA does not; GIT_DEPTH and GIT_SUBMODULE_STRATEGY map to the step inputs")
	return m.n, nil
}

// sourceCheckout reports whether a job of a Concourse source reads the repository that holds the pipeline, and the
// fetch depth that its get set (0 for the full history). The job has no checkout step because Concourse fetches
// the repository with the get.
func sourceCheckout(from *expr.Dialect, j *ir.Job) (int, bool) {
	if from == nil || from.ID != exprconcourse.Dialect || len(j.Steps) == 0 {
		return 0, false
	}
	if first := j.Steps[0]; first != nil && first.Kind == ir.StepAction && first.Action != nil && first.Action.Repo == "checkout" {
		return 0, false
	}
	ext, _ := j.Ext[exprconcourse.Dialect].(map[string]any)
	v, ok := ext[emitkit.CheckoutKey]
	if !ok {
		return 0, false
	}
	m, _ := v.(map[string]any)
	depth, _ := strconv.Atoi(fmt.Sprint(m["depth"]))
	return max(depth, 0), true
}

// sourceCheckoutNode is the checkout step for a job of a Concourse source.
func (e *emitter) sourceCheckoutNode(jp string, depth int) (*yaml.Node, error) {
	ref, err := reverseAction("checkout", "")
	if err != nil {
		return nil, err
	}
	m := newMap()
	uses, err := e.synthUses(jp, "checkout", ref)
	if err != nil {
		return nil, err
	}
	m.set("uses", str(uses))
	if depth > 0 {
		m.set("with", withOf("fetch-depth", intNode(depth)))
	}
	e.emulate(jp, caps.StepAction, "actions/checkout step", "Concourse fetches the repository with the get of the job and GHA does not; the depth param of the get maps to fetch-depth")
	return m.n, nil
}
