package woodpecker

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
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
	r.take("name")
	sv := ir.Service{Name: name}
	img, ok := text(m.vals["image"])
	if !ok {
		return sv, nil, fmt.Errorf("service %s: image is required", name)
	}
	r.take("image")
	ref, err := w.tx.template(img)
	if err != nil {
		return sv, nil, err
	}
	sv.Image.Ref = ref
	if v, ok := r.take("entrypoint"); ok {
		l, isList := textList(v)
		if !isList {
			return sv, nil, fmt.Errorf("service %s: entrypoint must be a string or a list", name)
		}
		for _, e := range l {
			t, err := w.tx.template(e)
			if err != nil {
				return sv, nil, err
			}
			sv.Image.Entrypoint = append(sv.Image.Entrypoint, t)
		}
	}
	if v, ok := r.take("ports"); ok {
		l, isList := textList(v)
		if !isList {
			return sv, nil, fmt.Errorf("service %s: ports must be a list", name)
		}
		sv.Ports = unescapeAll(l)
	}
	rest := &omap{vals: map[string]any{}, lines: map[string]int{}}
	if v, ok := r.take("environment"); ok {
		em, isMap := asMap(v)
		if !isMap {
			return sv, nil, fmt.Errorf("service %s: environment must be a mapping", name)
		}
		left := &omap{vals: map[string]any{}, lines: map[string]int{}}
		for _, k := range em.keys {
			if val, ok := w.envValue(em.vals[k]); ok {
				sv.Env = append(sv.Env, ir.Var{Name: k, Value: val})
				continue
			}
			left.set(k, em.vals[k], em.lines[k])
		}
		if len(left.keys) > 0 {
			rest.set("environment", left, r.line("environment"))
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
