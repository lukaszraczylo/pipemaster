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
	// gitlabDefaultCacheKey is the key GitLab gives a cache with no key; every such job shares it
	// (https://docs.gitlab.com/ci/yaml/#cachekey).
	gitlabDefaultCacheKey = "default"
	cacheScopeCaveat      = "GitHub restores only caches of the current branch, the base branch of a pull request and the default branch, while a GitLab key with no branch part is shared across branches"
	cacheRunSep           = "-run-"
	// cacheRunID is run_id plus run_attempt: run_id stays the same on a re-run, so it alone would repeat the key
	// (https://docs.github.com/en/actions/reference/workflows-and-actions/contexts).
	cacheRunID    = "${{ github.run_id }}-${{ github.run_attempt }}"
	hashFilesCall = "hashfiles("
	monthDays     = 30
	weekDays      = 7
	hoursPerDay   = 24
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

// preSteps are the steps that run before the job's own: the checkout, artifact downloads, then the cache restore; a push-only cache has no restore.
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
		if ch.Policy == cachePolicyPush {
			continue
		}
		n, err := e.cacheStep(jp, ch)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// cacheRollDetail names the effect of a rewritten key. GitHub scoping:
// https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching
func cacheRollDetail(shared bool) string {
	d := "the key is <key>-run-<run_id>-<run_attempt> and the restore prefix is <key>-run-: each run saves a new cache and restores the newest by prefix, because actions/cache never overwrites a key; two jobs of one run that share a key race on the same key and the first to save wins, where on GitLab the last upload wins; the new entries grow toward the 10 GB repository cache limit and evict the least recently used caches, including caches of other keys; " + cacheScopeCaveat
	if shared {
		d = "the source has no key, so GitLab shares one cache named default across jobs; " + d
	}
	return d
}

func withOf(kv ...any) *yaml.Node {
	m := newMap()
	for i := 0; i+1 < len(kv); i += 2 {
		m.set(kv[i].(string), kv[i+1].(*yaml.Node))
	}
	return m.n
}

func (e *emitter) cacheStep(jp string, ch ir.Cache) (*yaml.Node, error) {
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
		key = gitlabDefaultCacheKey
	}
	kn, restore, changed, err := e.cacheKeys(jp, key, ch.FallbackKeys)
	if err != nil {
		return nil, err
	}
	if changed {
		e.emulate(jp, caps.JobCache, "actions/cache step with a rolling key", cacheRollDetail(ch.Key == ""))
	}
	m := newMap()
	uses, err := e.synthUses(jp, "cache", ref)
	if err != nil {
		return nil, err
	}
	m.set("uses", str(uses))
	with := newMap()
	with.set("path", pathNode(ch.Paths))
	with.set("key", kn)
	if len(restore) > 0 && sub != "save" {
		with.set("restore-keys", block(restore))
	}
	m.set("with", with.n)
	return marked(m, emitkit.Marker(caps.JobCache, jp)), nil
}

// cacheKeys writes the key and the restore-keys. actions/cache never overwrites an existing key, but GitLab uploads
// the cache again after every run, so a key with no content hash becomes the rolling pair <key>-run-<run_id>-<run_attempt> and
// <key>-run-: each run saves a new entry and the next run restores the newest. The "-run-" part keeps the prefix of
// "main" from matching the keys of "main-hotfix". A hashFiles key already names its content. The bool is true
// when a key was rewritten.
func (e *emitter) cacheKeys(jp, key string, fallback []string) (*yaml.Node, []string, bool, error) {
	changed := false
	rolled := func(k string) (string, string, error) {
		t, err := e.template(jp, k)
		if err != nil {
			return "", "", err
		}
		if hashesContent(k) {
			return t, t, nil
		}
		changed = true
		return t + cacheRunSep + cacheRunID, t + cacheRunSep, nil
	}
	k, restore, err := rolled(key)
	if err != nil {
		return nil, nil, false, err
	}
	var restoreKeys []string
	if !hashesContent(key) {
		restoreKeys = append(restoreKeys, restore)
	}
	for _, fb := range fallback {
		_, r, err := rolled(fb)
		if err != nil {
			return nil, nil, false, err
		}
		restoreKeys = append(restoreKeys, r)
	}
	return str(k), restoreKeys, changed, nil
}

func hashesContent(key string) bool {
	return strings.Contains(strings.ToLower(key), hashFilesCall)
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

// postSteps are the cache saves of a push policy and the uploads that close the job.
func (e *emitter) postSteps(jp string, j *ir.Job) ([]*yaml.Node, error) {
	var out []*yaml.Node
	for _, ch := range j.Cache {
		if ch.Policy != cachePolicyPush {
			continue
		}
		n, err := e.cacheStep(jp, ch)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
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

// checkoutStep is the clone that the CI system of the source runs before the steps, which a GHA job must ask for.
// It is nil when the origin does not clone for the job. The step is counted as a pre step, so Parse removes it again.
func (e *emitter) checkoutStep(c jobCtx, jp string, j *ir.Job) (*yaml.Node, error) {
	co, ok := emitkit.SourceCheckout(e.origin, c.pl, j)
	if !ok {
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
	if co.Depth > 0 {
		with.set("fetch-depth", intNode(co.Depth))
	}
	switch co.Submodules {
	case emitkit.SubmodulesOn:
		with.set("submodules", boolNodeOf(true))
	case emitkit.SubmodulesDeep:
		with.set("submodules", str(emitkit.SubmodulesDeep))
	}
	if len(with.n.Content) > 0 {
		m.set("with", with.n)
	}
	e.emulate(jp, caps.StepAction, "actions/checkout step", emitkit.SourceCheckoutDetail(e.origin))
	return m.n, nil
}
