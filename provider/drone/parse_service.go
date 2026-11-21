package drone

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/container"
)

// services reads the service list. Keys without a Service field stay in Ext under services.<name>.
func (d *doc) services(v any) error {
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("services must be a list")
	}
	extra := &omap{vals: map[string]any{}, lines: map[string]int{}}
	for i, e := range list {
		m, ok := asMap(e)
		if !ok {
			return fmt.Errorf("service %d must be a mapping", i+1)
		}
		name, _ := text(m.vals["name"])
		if name == "" {
			return fmt.Errorf("service %d needs a name", i+1)
		}
		sv, rest, err := d.service(name, m)
		if err != nil {
			return err
		}
		d.job.Services = append(d.job.Services, sv)
		if len(rest.keys) > 0 {
			extra.set(name, rest, 0)
		}
	}
	if len(extra.keys) > 0 {
		d.ext.vals["services"] = plain(extra)
	}
	return nil
}

func (d *doc) service(name string, m *omap) (ir.Service, *omap, error) {
	r := newReader(m)
	sv, leftEnv, err := container.Service(d.decoder(), name, dollarEsc, r.take)
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
		d.ext.note(caps.JobServices, "service "+name+": key "+k+" has no IR field; kept in Ext", rest.lines[k])
	}
	return sv, rest, nil
}
