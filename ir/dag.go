package ir

import (
	"errors"
	"fmt"
	"sort"
)

var (
	// ErrCycle reports a cycle in the job needs graph.
	ErrCycle = errors.New("needs cycle")
	// ErrUnknownNeed reports a required need that names no job.
	ErrUnknownNeed = errors.New("needs unknown job")
)

// isDanglingNeed is true for a required need on a missing job; an optional one adds no edge.
func isDanglingNeed(byID map[string]*Job, n Need) bool {
	_, ok := byID[n.JobID]
	return !ok && !n.Optional
}

// Layers groups jobs by longest needs-path depth; IDs are sorted within a layer.
// Empty input yields a non-nil empty slice.
func Layers(jobs []*Job) ([][]string, error) {
	byID := map[string]*Job{}
	for _, j := range jobs {
		if j == nil {
			continue
		}
		if _, dup := byID[j.ID]; !dup {
			byID[j.ID] = j
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	indeg := map[string]int{}
	dependents := map[string][]string{}
	for _, id := range ids {
		for _, n := range byID[id].Needs {
			if isDanglingNeed(byID, n) {
				return nil, fmt.Errorf("job %q needs %q: %w", id, n.JobID, ErrUnknownNeed)
			}
			if _, ok := byID[n.JobID]; !ok {
				continue
			}
			indeg[id]++
			dependents[n.JobID] = append(dependents[n.JobID], id)
		}
	}

	layerOf := map[string]int{}
	var queue []string
	for _, id := range ids {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	placed := 0
	depth := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		placed++
		if layerOf[id]+1 > depth {
			depth = layerOf[id] + 1
		}
		for _, d := range dependents[id] {
			if layerOf[id]+1 > layerOf[d] {
				layerOf[d] = layerOf[id] + 1
			}
			if indeg[d]--; indeg[d] == 0 {
				queue = append(queue, d)
			}
		}
	}
	if placed < len(ids) {
		cyc := findCycles(byID)[0]
		return nil, fmt.Errorf("job %q is on a cycle (%s): %w", cyc[0], joinPath(cyc), ErrCycle)
	}

	out := make([][]string, depth)
	for _, id := range ids {
		out[layerOf[id]] = append(out[layerOf[id]], id)
	}
	return out, nil
}
