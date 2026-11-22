package argo

import (
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// The pipemaster Sensor format. A Sensor dependency carries one GitHub event class and its filters:
//   - a push trigger with branches: data filter body.ref with the anchored regex ^refs/heads/<glob>$ per include glob;
//   - a tag trigger: data filter body.ref with ^refs/tags/<glob>$ per include glob, or ^refs/tags/.*$ with none;
//   - a pull request trigger: data filter body.action with the IR types as ^<type>$ (opened, reopened and synchronize when none), and body.pull_request.base.ref
//     with the anchored regex ^<glob>$ per include glob;
//   - an exclude: one exprs entry per excluded name, `ref != 'refs/heads/<name>'` (tags: refs/tags/, pull request: base != '<name>');
//   - a pull request gate: one exprs entry `head == base` over body.pull_request.head.repo.full_name and
//     body.repository.full_name, so a pull request from a fork never starts a run.
const (
	eventSourceName = "pipemaster-github"
	sensorName      = "pipemaster"
	webhookSecret   = "pipemaster-github-webhook"
	webhookKey      = "token"
	workflowSA      = "pipemaster-workflow"
	sensorSA        = "pipemaster-sensor"
	webhookPort     = "12000"

	exprFork     = "head == base"
	exprRefName  = "ref"
	exprBaseName = "base"
	fieldHead    = "head"
	pathHeadRepo = "body.pull_request.head.repo.full_name"
	pathRepo     = "body.repository.full_name"
)

var (
	globLiteral = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	actionName  = regexp.MustCompile(`^[a-z_]+$`)
)

const (
	extGHA      = "gha"
	extTypes    = "types"
	extActions  = "actions"
	typesNoForm = "a pull request type is not a webhook action name"
)

// anyStrings reads a string or a list of strings from Ext data.
func anyStrings(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil
			}
			out = append(out, s)
		}
		return out
	}
	return nil
}

// prActions returns the Sensor values of the pull request actions of a trigger. The default set keeps plain values. The default holds when no source names a type. why is set for a type that
// has no action name.
func prActions(t ir.Trigger) (values []string, why string) {
	var types []string
	if m, ok := t.Ext[dialect].(map[string]any); ok {
		types = anyStrings(m[extActions])
	}
	if len(types) == 0 {
		if m, ok := t.Ext[extGHA].(map[string]any); ok {
			types = anyStrings(m[extTypes])
		}
	}
	if len(types) == 0 || sameSet(types, defaultPRActions) {
		return defaultPRActions, ""
	}
	for _, ty := range types {
		if !actionName.MatchString(ty) {
			return nil, typesNoForm
		}
		values = append(values, regexAnchorBeg+ty+regexAnchorEnd)
	}
	return values, ""
}

// withoutTypes is x without the GHA types, which a Sensor action filter states.
func withoutTypes(x ir.Ext) ir.Ext {
	m, ok := x[extGHA].(map[string]any)
	if !ok {
		return x
	}
	out := ir.Ext{}
	for k, v := range x {
		out[k] = v
	}
	rest := map[string]any{}
	for k, v := range m {
		if k != extTypes {
			rest[k] = v
		}
	}
	out[extGHA] = rest
	return out
}

// globRegex writes a branch or tag glob as an anchored-body regex; ok is false when the glob has no exact regex.
func globRegex(g string) (string, bool) {
	prefix, star := strings.CutSuffix(g, globStar)
	switch {
	case !star && globLiteral.MatchString(g):
		return regexp.QuoteMeta(g), true
	case star && (prefix == "" || globLiteral.MatchString(prefix)) && !strings.Contains(prefix, globStar):
		return regexp.QuoteMeta(prefix) + globAnyTail, true
	}
	return "", false
}

// literalName is true for an excluded name that an equality test can state.
func literalName(g string) bool { return globLiteral.MatchString(g) }

func dataFilterNode(path string, values []string) map[string]any {
	vs := make([]any, len(values))
	for i, v := range values {
		vs[i] = v
	}
	return map[string]any{"path": path, "type": typeString, "value": vs}
}

func exprNode(expr string, fields ...[2]string) map[string]any {
	fs := make([]any, len(fields))
	for i, f := range fields {
		fs[i] = map[string]any{"name": f[0], "path": f[1]}
	}
	return map[string]any{"expr": expr, "fields": fs}
}

func anchored(prefix string, globs []string) ([]string, bool) {
	var out []string
	for _, g := range globs {
		re, ok := globRegex(g)
		if !ok {
			return nil, false
		}
		out = append(out, regexAnchorBeg+prefix+re+regexAnchorEnd)
	}
	return out, true
}

// triggerFilters builds the filters of the dependency of one IR trigger. why is empty when it succeeded.
func triggerFilters(t ir.Trigger) (filters map[string]any, event, why string) {
	var data, exprs []any
	refFilter := func(prefix string, f ir.Filter) string {
		vals, ok := anchored(prefix, f.Include)
		if !ok {
			return "a glob has no exact regex"
		}
		switch {
		case len(vals) > 0:
			data = append(data, dataFilterNode(pathRef, vals))
		case t.Kind == ir.TriggerTag:
			data = append(data, dataFilterNode(pathRef, []string{regexAnchorBeg + prefix + globAnyTail + regexAnchorEnd}))
		}
		for _, x := range f.Exclude {
			if !literalName(x) {
				return "the excluded name " + x + " is a glob, which an equality test cannot state"
			}
			exprs = append(exprs, exprNode(exprRefName+" != '"+prefix+x+"'", [2]string{exprRefName, pathRef}))
		}
		return ""
	}
	switch t.Kind {
	case ir.TriggerPush:
		event = eventPush
		if why = refFilter(refHeads, t.Branches); why != "" {
			return nil, "", why
		}
	case ir.TriggerTag:
		event = eventPush
		if why = refFilter(refTags, t.Tags); why != "" {
			return nil, "", why
		}
	case ir.TriggerPR:
		event = eventPR
		actions, bad := prActions(t)
		if bad != "" {
			return nil, "", bad
		}
		data = append(data, dataFilterNode(pathAction, actions))
		vals, ok := anchored("", t.Branches.Include)
		if !ok {
			return nil, "", "a glob has no exact regex"
		}
		if len(vals) > 0 {
			data = append(data, dataFilterNode(pathBaseRef, vals))
		}
		for _, x := range t.Branches.Exclude {
			if !literalName(x) {
				return nil, "", "the excluded name " + x + " is a glob, which an equality test cannot state"
			}
			exprs = append(exprs, exprNode(exprBaseName+" != '"+x+"'", [2]string{exprBaseName, pathBaseRef}))
		}
		exprs = append(exprs, exprNode(exprFork, [2]string{fieldHead, pathHeadRepo}, [2]string{exprBaseName, pathRepo}))
	default:
		return nil, "", "the trigger kind has no GitHub event"
	}
	filters = map[string]any{}
	if len(data) > 0 {
		filters["data"] = data
	}
	if len(exprs) > 0 {
		filters["exprs"] = exprs
	}
	return filters, event, ""
}
