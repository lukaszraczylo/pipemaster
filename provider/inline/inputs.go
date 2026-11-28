package inline

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	extEvent       = "event"
	eventCall      = "workflow_call"
	keyInputs      = "inputs"
	keySecrets     = "secrets"
	secretsInherit = "inherit"
	typeBool       = "boolean"
	typeNumber     = "number"
)

// callInputs holds the arguments of one reusable workflow call: the value of each input, and the secrets map.
type callInputs struct {
	vals    map[string]ir.Value
	types   map[string]string
	secrets map[string]ir.Value
	unused  []string
	active  bool
	inherit bool
}

// callArgs reads the arguments of a reusable workflow call. A GitLab trigger job has none: the child reads the
// variables it gets as variables.
func callArgs(j *ir.Job, child *ir.Pipeline) *callInputs {
	c := &callInputs{}
	if callKind(j) == kindGitLab || len(j.Steps) != 1 || j.Steps[0] == nil || j.Steps[0].Kind != ir.StepPipelineCall {
		return c
	}
	c.active = true
	st := j.Steps[0]
	decl := declaredInputs(child)
	c.vals = map[string]ir.Value{}
	c.types = map[string]string{}
	for name, d := range decl {
		typ, _ := d["type"].(string)
		c.types[name] = typ
		if v, ok := st.With[name]; ok {
			c.vals[name] = v
			continue
		}
		c.vals[name] = ir.Value{Lit: defaultText(d["default"])}
	}
	for name := range st.With {
		if _, ok := decl[name]; !ok {
			c.unused = append(c.unused, name)
		}
	}
	sort.Strings(c.unused)
	c.secrets = map[string]ir.Value{}
	for _, e := range st.Ext {
		m, _ := e.(map[string]any)
		switch s := m[keySecrets].(type) {
		case string:
			c.inherit = s == secretsInherit
		case map[string]any:
			for name, v := range s {
				c.secrets[name] = textValue(fmt.Sprint(v))
			}
		}
	}
	return c
}

// declaredInputs reads the inputs of the on.workflow_call block that the parser kept in the trigger Ext.
func declaredInputs(child *ir.Pipeline) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, t := range child.Triggers {
		for _, e := range t.Ext {
			m, _ := e.(map[string]any)
			if ev, _ := m[extEvent].(string); ev != eventCall {
				continue
			}
			ins, _ := m[keyInputs].(map[string]any)
			for name, v := range ins {
				d, _ := v.(map[string]any)
				if d == nil {
					d = map[string]any{}
				}
				out[name] = d
			}
		}
	}
	return out
}

func defaultText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// textValue reads the text of a with: or secrets: argument: one expression is an expression, one secrets
// reference is a secret, anything else is text.
func textValue(s string) ir.Value {
	if !strings.Contains(s, "${{") {
		return ir.Value{Lit: s}
	}
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil || len(parts) != 1 || parts[0].Expr == nil {
		return ir.Value{Lit: s}
	}
	if r, ok := parts[0].Expr.(expr.Ref); ok && len(r.Path) == 2 && r.Path[0] == expr.PrefixSecrets {
		return ir.Value{Kind: ir.ValueSecret, Secret: r.Path[1]}
	}
	return ir.Value{Kind: ir.ValueExpr, Expr: parts[0].Expr}
}

// apply replaces the input and secret references of one inlined job by what the call passed.
func (c *callInputs) apply(nj *ir.Job) {
	if !c.active {
		return
	}
	inner := nj.Child
	nj.Child = nil
	reflectWalk(c, reflect.ValueOf(nj))
	nj.Child = inner
}

var (
	exprType  = reflect.TypeOf((*expr.Expr)(nil)).Elem()
	valueType = reflect.TypeOf(ir.Value{})
	childType = reflect.TypeOf((*ir.Child)(nil))
)

func reflectWalk(c *callInputs, v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() && v.Type() != childType {
			reflectWalk(c, v.Elem())
		}
	case reflect.Interface:
		if v.IsNil() || !v.CanSet() {
			return
		}
		if v.Type() == exprType {
			folded := expr.FoldConst(c.expr(v.Interface().(expr.Expr), false))
			if l, ok := folded.(expr.Lit); ok && expr.LitTruthy(l) {
				v.Set(reflect.Zero(v.Type()))
				return
			}
			v.Set(reflect.ValueOf(folded))
			return
		}
		cp := reflect.New(v.Elem().Type()).Elem()
		cp.Set(v.Elem())
		reflectWalk(c, cp)
		v.Set(cp)
	case reflect.Struct:
		if v.Type() == valueType && v.CanSet() {
			v.Set(reflect.ValueOf(c.value(v.Interface().(ir.Value))))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				reflectWalk(c, v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			reflectWalk(c, v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			cp := reflect.New(v.Type().Elem()).Elem()
			cp.Set(v.MapIndex(k))
			reflectWalk(c, cp)
			v.SetMapIndex(k, cp)
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(c.text(v.String()))
		}
	}
}

// expr rewrites the references of one expression. A typed input becomes a typed literal in a condition, and
// text in a template.
func (c *callInputs) expr(e expr.Expr, asText bool) expr.Expr {
	switch v := e.(type) {
	case expr.Ref:
		return c.ref(v, asText)
	case expr.Unary:
		return expr.Unary{Op: v.Op, X: c.expr(v.X, false)}
	case expr.Binary:
		return expr.Binary{Op: v.Op, L: c.expr(v.L, false), R: c.expr(v.R, false)}
	case expr.Call:
		args := make([]expr.Expr, len(v.Args))
		for i, a := range v.Args {
			args[i] = c.expr(a, false)
		}
		return expr.Call{Name: v.Name, Args: args}
	}
	return e
}

func (c *callInputs) ref(r expr.Ref, asText bool) expr.Expr {
	if len(r.Path) != 2 {
		return r
	}
	switch r.Path[0] {
	case expr.PrefixInputs:
		v, ok := c.vals[r.Path[1]]
		if !ok {
			return r
		}
		return c.valueExpr(v, c.types[r.Path[1]], asText)
	case expr.PrefixSecrets:
		if c.inherit {
			return r
		}
		v, ok := c.secrets[r.Path[1]]
		if !ok {
			return expr.Lit{Kind: expr.LitStr}
		}
		return c.valueExpr(v, "", asText)
	}
	return r
}

func (c *callInputs) valueExpr(v ir.Value, typ string, asText bool) expr.Expr {
	switch v.Kind {
	case ir.ValueSecret:
		return expr.Ref{Path: []string{expr.PrefixSecrets, v.Secret}}
	case ir.ValueExpr:
		return v.Expr
	}
	if strings.Contains(v.Lit, "${{") {
		if parts, err := expr.ParseScriptTemplate(v.Lit); err == nil {
			return expr.JoinParts(parts)
		}
	}
	if asText {
		return expr.Lit{Kind: expr.LitStr, Str: v.Lit}
	}
	switch typ {
	case typeBool:
		if b, err := strconv.ParseBool(v.Lit); err == nil {
			return expr.Lit{Kind: expr.LitBool, Bool: b}
		}
		return expr.Lit{Kind: expr.LitBool}
	case typeNumber:
		if n, err := strconv.ParseFloat(v.Lit, 64); err == nil {
			return expr.Lit{Kind: expr.LitNum, Num: n}
		}
	}
	return expr.Lit{Kind: expr.LitStr, Str: v.Lit}
}

// value rewrites a value: a secret reference maps to the secret that the call passed, an expression or a
// template is rewritten in place.
func (c *callInputs) value(v ir.Value) ir.Value {
	switch v.Kind {
	case ir.ValueSecret:
		if c.inherit {
			return v
		}
		if m, ok := c.secrets[v.Secret]; ok {
			return m
		}
		return ir.Value{}
	case ir.ValueExpr:
		return fromExpr(c.expr(v.Expr, false))
	}
	if !strings.Contains(v.Lit, "${{") {
		return v
	}
	parts, err := expr.ParseScriptTemplate(v.Lit)
	if err != nil {
		return v
	}
	if len(parts) == 1 && parts[0].Expr != nil {
		return fromExpr(c.expr(parts[0].Expr, false))
	}
	return ir.Value{Lit: c.text(v.Lit)}
}

// fromExpr turns a rewritten expression back into the narrowest value.
func fromExpr(e expr.Expr) ir.Value {
	switch x := e.(type) {
	case expr.Lit:
		return ir.Value{Lit: litText(x)}
	case expr.Ref:
		if len(x.Path) == 2 && x.Path[0] == expr.PrefixSecrets {
			return ir.Value{Kind: ir.ValueSecret, Secret: x.Path[1]}
		}
	}
	return ir.Value{Kind: ir.ValueExpr, Expr: e}
}

func litText(l expr.Lit) string {
	switch l.Kind {
	case expr.LitBool:
		return strconv.FormatBool(l.Bool)
	case expr.LitNum:
		return strconv.FormatFloat(l.Num, 'f', -1, 64)
	case expr.LitStr:
		return l.Str
	}
	return ""
}

// text rewrites the references inside the ${{ }} parts of a template; the text around them stays as written.
func (c *callInputs) text(s string) string {
	if !strings.Contains(s, "${{") {
		return s
	}
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return s
	}
	changed := false
	for i, p := range parts {
		if p.Expr == nil {
			continue
		}
		n := c.expr(p.Expr, true)
		if reflect.DeepEqual(n, p.Expr) {
			continue
		}
		changed = true
		if l, ok := n.(expr.Lit); ok {
			parts[i] = expr.Part{Text: litText(l)}
			continue
		}
		parts[i] = expr.Part{Expr: n}
	}
	if !changed {
		return s
	}
	out, err := expr.PrintScriptTemplate(expr.MergeText(parts))
	if err != nil {
		return s
	}
	return out
}
