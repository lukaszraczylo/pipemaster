package ir

import (
	"errors"
	"fmt"
	"sort"
	"strings"
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
			if s == nil || s.ID == "" {
				continue
			}
			if seenS[s.ID] {
				add(jobPath(pl.ID, j.ID)+"/step/"+s.ID, "duplicate step id")
			}
			seenS[s.ID] = true
		}
	}
	for _, j := range all {
		if j == nil || j.ID == "" {
			continue
		}
		for _, n := range j.Needs {
			if _, ok := byID[n.JobID]; !ok {
				add(jobPath(pl.ID, j.ID), "needs unknown job %q", n.JobID)
			}
		}
	}
	for _, cyc := range findCycles(byID) {
		add(pipelinePath(pl.ID), "needs cycle: %s", strings.Join(cyc, " -> "))
	}
}

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
