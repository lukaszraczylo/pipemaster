package woodpecker

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/container"
)

// services reads the service list or map. Keys without a Service field stay in Ext under services.<name>.
func (w *wf) services(j *ir.Job, v any) error {
	extra := &omap{vals: map[string]any{}, lines: map[string]int{}}
	add := func(name string, m *omap, line int) error {
		if name == "" {
			return fmt.Errorf("line %d: a service needs a name", line)
		}
		sv, rest, err := w.service(name, m)
		if err != nil {
			return err
		}
		j.Services = append(j.Services, sv)
		if len(rest.keys) > 0 {
			extra.set(name, rest, line)
		}
		return nil
	}
	switch t := v.(type) {
	case []any:
		for i, e := range t {
			m, ok := asMap(e)
			if !ok {
				return fmt.Errorf("service %d must be a mapping", i+1)
			}
			name, _ := text(m.vals["name"])
			if err := add(name, m, 0); err != nil {
				return err
			}
		}
	case *omap:
		for _, k := range t.keys {
			m, ok := asMap(t.vals[k])
			if !ok {
				return fmt.Errorf("line %d: service %s must be a mapping", t.lines[k], k)
			}
			if err := add(k, m, t.lines[k]); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("services must be a list or a mapping")
	}
	if len(extra.keys) > 0 {
		w.ext.vals["services"] = plain(extra)
	}
	return nil
}

func (w *wf) service(name string, m *omap) (ir.Service, *omap, error) {
	r := newReader(m)
	sv, leftEnv, err := container.Service(w.decoder(), name, dollarEsc, r.take)
	if err != nil {
		return sv, nil, err
	}
	rest := &omap{vals: map[string]any{}, lines: map[string]int{}}
	if len(leftEnv) > 0 {
		if em, ok := asMap(m.vals["environment"]); ok {
			rest.set("environment", em.subset(leftEnv), r.line("environment"))
		}
	}
	for _, k := range r.rest() {
		rest.set(k, m.vals[k], m.lines[k])
	}
	for _, k := range rest.keys {
		w.ext.note(caps.JobServices, "service "+name+": key "+k+" has no IR field; kept in Ext", rest.lines[k])
	}
	return sv, rest, nil
}
