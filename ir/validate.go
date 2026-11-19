package ir

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Error is a validation failure at an IR path.
type Error struct {
	Path string
	Msg  string
}

func (e *Error) Error() string { return e.Path + ": " + e.Msg }

func pipelinePath(id string) string  { return "pipeline/" + id }
func jobPath(pid, jid string) string { return pipelinePath(pid) + "/job/" + jid }

// Validate checks IDs and the needs graph; it returns every problem joined.
func Validate(p *Project) error {
	if p == nil {
		return nil
	}
	var errs []error
	add := func(path, format string, a ...any) {
		errs = append(errs, &Error{Path: path, Msg: fmt.Sprintf(format, a...)})
	}
	seenP := map[string]bool{}
	for _, pl := range p.Pipelines {
		if pl == nil {
			continue
		}
		switch {
		case pl.ID == "":
			add("pipeline/", "empty pipeline id")
		case seenP[pl.ID]:
			add(pipelinePath(pl.ID), "duplicate pipeline id")
		}
		seenP[pl.ID] = true
		validatePipeline(pl, add)
	}
	return errors.Join(errs...)
}

func validatePipeline(pl *Pipeline, add func(path, format string, a ...any)) {
	all := append(append([]*Job{}, pl.Jobs...), pl.Finally...)
	byID := map[string]*Job{}
	for _, j := range all {
		if j == nil {
			continue
		}
		if j.ID == "" {
			add(pipelinePath(pl.ID), "empty job id")
			continue
		}
		if _, dup := byID[j.ID]; dup {
			add(jobPath(pl.ID, j.ID), "duplicate job id")
			continue
		}
		byID[j.ID] = j
		seenS := map[string]bool{}
		for _, s := range j.Steps {
			if s != nil && s.Action != nil && !ValidHost(s.Action.Host) {
				add(stepPath(pl.ID, j.ID, s.ID), "action host %q must be a http(s) base URL, %q or %q", s.Action.Host, HostSelf, HostBuiltin)
			}
			if s == nil || s.ID == "" {
				continue
			}
			if seenS[s.ID] {
				add(stepPath(pl.ID, j.ID, s.ID), "duplicate step id")
			}
			seenS[s.ID] = true
		}
	}
	checkVars(pipelinePath(pl.ID), "env", pl.Env, add)
	for _, j := range all {
		if j != nil && j.ID != "" {
			validateExprs(pl.ID, j, add)
		}
	}
	for _, j := range all {
		if j == nil || j.ID == "" {
			continue
		}
		for _, n := range j.Needs {
			if isDanglingNeed(byID, n) {
				add(jobPath(pl.ID, j.ID), "needs unknown job %q", n.JobID)
			}
		}
	}
	for _, cyc := range findCycles(byID) {
		add(pipelinePath(pl.ID), "needs cycle: %s", joinPath(cyc))
	}
}

// validateExprs reports every Expr that has no canonical text, so Marshal cannot fail on a validated project.
func validateExprs(pid string, j *Job, add func(path, format string, a ...any)) {
	jp := jobPath(pid, j.ID)
	checkExpr(jp+"/if", j.If, add)
	checkVars(jp, "env", j.Env, add)
	for _, sv := range j.Services {
		checkVars(jp+"/service/"+sv.Name, "env", sv.Env, add)
	}
	for i, s := range j.Steps {
		if s == nil {
			continue
		}
		id := s.ID
		if id == "" {
			id = fmt.Sprintf("#%d", i)
		}
		sp := stepPath(pid, j.ID, id)
		checkExpr(sp+"/if", s.If, add)
		checkVars(sp, "env", s.Env, add)
		keys := make([]string, 0, len(s.With))
		for k := range s.With {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			checkValue(sp+"/with/"+k, s.With[k], add)
		}
	}
}

func checkVars(path, field string, vs []Var, add func(path, format string, a ...any)) {
	for _, v := range vs {
		checkValue(path+"/"+field+"/"+v.Name, v.Value, add)
	}
}

func checkValue(path string, v Value, add func(path, format string, a ...any)) {
	switch v.Kind {
	case ValueLit, ValueSecret:
	case ValueExpr:
		if err := expr.Check(v.Expr); err != nil {
			add(path, "invalid expression: %v", err)
		}
	default:
		add(path, "invalid value kind %d", v.Kind)
	}
}

func checkExpr(path string, e expr.Expr, add func(path, format string, a ...any)) {
	if e == nil {
		return
	}
	if err := expr.Check(e); err != nil {
		add(path, "invalid expression: %v", err)
	}
}

func joinPath(cyc []string) string { return strings.Join(cyc, " -> ") }

const (
	unvisited = iota
	visiting
	done
)

// findCycles reports one path per back edge, walking jobs and edges in sorted order for stable output.
func findCycles(byID map[string]*Job) [][]string {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	state := map[string]int{}
	var stack []string
	var out [][]string
	var visit func(id string)
	visit = func(id string) {
		state[id] = visiting
		stack = append(stack, id)
		deps := make([]string, 0, len(byID[id].Needs))
		for _, n := range byID[id].Needs {
			if _, ok := byID[n.JobID]; ok {
				deps = append(deps, n.JobID)
			}
		}
		sort.Strings(deps)
		for _, d := range deps {
			switch state[d] {
			case unvisited:
				visit(d)
			case visiting:
				start := 0
				for i, s := range stack {
					if s == d {
						start = i
					}
				}
				cyc := append(append([]string{}, stack[start:]...), d)
				out = append(out, cyc)
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = done
	}
	for _, id := range ids {
		if state[id] == unvisited {
			visit(id)
		}
	}
	return out
}

// ValidHost reports whether h is empty, a Gitea prefix form or an http(s) base URL with no path.
func ValidHost(h string) bool {
	if h == "" || h == HostSelf || h == HostBuiltin {
		return true
	}
	u, err := url.Parse(h)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.Path == "" &&
		u.RawQuery == "" && u.Fragment == "" && u.User == nil && !strings.HasSuffix(h, "/")
}
