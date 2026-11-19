package gha

import (
	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// scopeAll is the Permissions.Scopes key for the read-all and write-all shorthands.
const (
	scopeAll = "*"
	idToken  = "id-token"

	extConcurrency = "concurrency"
)

// pipelineFields consumes the workflow-level keys that map to IR fields.
func (fp *fileParser) pipelineFields(pl *ir.Pipeline, m *mapping) (err error) {
	if v := m.take("name"); v != nil {
		if pl.Name, err = fp.scalar(v, "name"); err != nil {
			return err
		}
	}
	if v := m.take("on"); v != nil {
		res, err := fp.parseOn(v)
		if err != nil {
			return err
		}
		pl.Triggers, pl.Inputs = res.triggers, res.inputs
		if res.other != nil {
			m.keep(extEvents, res.other)
		}
	}
	if err := fp.takeVars(m, "env", &pl.Env); err != nil {
		return err
	}
	if v := m.take("permissions"); v != nil {
		var ok bool
		if pl.Permissions, ok = fp.permissions(v); !ok {
			m.release("permissions")
		}
	}
	if key := fp.d.OIDCKey; key != "" {
		fp.oidcKey(pl, m, key)
	}
	if v := m.take("concurrency"); v != nil {
		if pl.Concurrency, err = fp.concurrency(m, v); err != nil {
			return err
		}
	}
	return nil
}

// oidcKey reads the dialect's workflow-level OIDC switch as Permissions.OIDC. Only an explicit true is modelled;
// any other value stays in Ext so it returns as written.
func (fp *fileParser) oidcKey(pl *ir.Pipeline, m *mapping, key string) {
	v := m.take(key)
	if v == nil {
		return
	}
	if on, ok := boolNode(v); !ok || !on {
		m.release(key)
		return
	}
	if pl.Permissions == nil {
		pl.Permissions = &ir.Permissions{}
	}
	pl.Permissions.OIDC = true
}

// permissions maps the read-all/write-all shorthands to Scopes["*"] and id-token: write to OIDC.
// An empty mapping stays a non-nil Permissions, which means "no permissions".
func (fp *fileParser) permissions(n *yaml.Node) (*ir.Permissions, bool) {
	if s, ok := scalarText(n); ok {
		switch s {
		case "read-all":
			return &ir.Permissions{Scopes: map[string]string{scopeAll: "read"}}, true
		case "write-all":
			return &ir.Permissions{Scopes: map[string]string{scopeAll: "write"}}, true
		}
		return nil, false
	}
	pm, err := fp.mapping(n, "permissions")
	if err != nil {
		return nil, false
	}
	p := &ir.Permissions{}
	for _, e := range pm.entries {
		level, ok := scalarText(e.val)
		if !ok || (e.key == scopeAll) {
			return nil, false
		}
		if e.key == idToken && level == "write" {
			p.OIDC = true
			continue
		}
		if p.Scopes == nil {
			p.Scopes = map[string]string{}
		}
		p.Scopes[e.key] = level
	}
	return p, true
}

// concurrency maps the group/cancel-in-progress forms; an expression cancel-in-progress stays in Ext.
func (fp *fileParser) concurrency(m *mapping, n *yaml.Node) (*ir.Concurrency, error) {
	if s, ok := scalarText(n); ok {
		g, err := fp.templateString(n, s)
		return &ir.Concurrency{Group: g}, err
	}
	cm, err := fp.mapping(n, "concurrency")
	if err != nil {
		return nil, err
	}
	c := &ir.Concurrency{}
	if g := cm.take("group"); g != nil {
		if c.Group, err = fp.template(g, "concurrency.group"); err != nil {
			return nil, err
		}
	}
	if v := cm.take("cancel-in-progress"); v != nil {
		b, ok := boolNode(v)
		if !ok {
			cm.release("cancel-in-progress")
		}
		c.CancelInProgress = b
	}
	rest, err := cm.rest()
	if rest != nil {
		m.keep(extConcurrency, rest)
	}
	return c, err
}
