package ynode

import (
	"time"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// ValueOf reads a key of a possibly nil mapping.
func ValueOf(m *Map, key string) any {
	if m == nil {
		return nil
	}
	return m.Vals[key]
}

// StringList reads a YAML sequence of scalars. A bare scalar is no list: it may be a reference to an array param.
func StringList(v any) ([]string, bool) {
	if _, isSeq := v.([]any); !isSeq {
		return nil, false
	}
	return TextList(v)
}

// Duration reads a positive Go duration such as "30m".
func Duration(v any) (ir.Duration, bool) {
	t, ok := Text(v)
	if !ok {
		return 0, false
	}
	d, err := time.ParseDuration(t)
	if err != nil || d <= 0 {
		return 0, false
	}
	return ir.Duration(d), true
}

// ContainerPorts reads ports with only a containerPort; whole is false when another key would be lost.
func ContainerPorts(v any) (ports []string, whole bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	for _, e := range list {
		m, ok := AsMap(e)
		if !ok || len(m.Order) != 1 || m.Order[0] != "containerPort" {
			return nil, false
		}
		t, ok := Text(m.Vals["containerPort"])
		if !ok {
			return nil, false
		}
		ports = append(ports, t)
	}
	return ports, true
}
