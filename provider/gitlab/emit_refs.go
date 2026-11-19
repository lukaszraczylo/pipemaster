package gitlab

import (
	"encoding/json"
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// Pair kinds: a canonical template, or an expression that became a whole value.
const (
	pairText = "t"
	pairExpr = "x"
)

// refPair records that the emitter wrote GitLab text G for the source S. Parse restores S only where a
// value or line equals G exactly, so edited text stays as the file holds it.
type refPair struct {
	Kind string `json:"k"`
	G    string `json:"g"`
	S    string `json:"s"`
}

// refPairs lists every flattening conversion in the pipeline: references such as matrix.x, inputs.x or
// env.x all print as $x, so the namespace cannot be read back from the text alone.
// A text that two different sources convert to is dropped, because no restore would be sound.
func refPairs(pl *ir.Pipeline) []refPair {
	byG := map[string]refPair{}
	clash := map[string]bool{}
	add := func(p refPair) {
		if p.G == p.S && p.Kind == pairText {
			return
		}
		if old, ok := byG[p.G]; ok && old != p {
			clash[p.G] = true
		}
		byG[p.G] = p
	}
	text := func(s string) {
		if parts, err := expr.ParseScriptTemplate(s); err != nil || !flattens(parts) {
			return
		}
		if t, lost, err := convert(s); err == nil && len(lost) == 0 && t != s {
			if c, err := canonText(t); err == nil {
				add(refPair{pairText, c, s})
			}
		}
	}
	vars := func(vs []ir.Var) {
		for _, v := range vs {
			switch v.Value.Kind {
			case ir.ValueLit:
				text(v.Value.Lit)
			case ir.ValueExpr:
				if !flattens([]expr.Part{{Expr: v.Value.Expr}}) && !flattensFormat(v.Value.Expr) {
					continue
				}
				if t, lost, err := exprText(v.Value.Expr); err == nil && len(lost) == 0 {
					if c, err := canonText(t); err == nil {
						add(refPair{pairExpr, c, expr.String(v.Value.Expr)})
					}
				}
			}
		}
	}
	vars(pl.Env)
	for _, j := range allJobs(pl) {
		vars(j.Env)
		for _, l := range j.Runner.Labels {
			text(l)
		}
		for _, st := range j.Steps {
			vars(st.Env)
			for _, l := range st.Script {
				text(l)
			}
		}
	}
	var out []refPair
	for g, p := range byG {
		if !clash[g] {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b refPair) int {
		if a.G != b.G {
			return compareStr(a.G, b.G)
		}
		return compareStr(a.S, b.S)
	})
	return out
}

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// flattensRoots are the namespaces whose references all print as one bare variable name.
var flattensRoots = []string{expr.PrefixEnv, expr.PrefixSecrets, expr.PrefixVars, expr.PrefixInputs, expr.PrefixMatrix, expr.PrefixNeeds}

// flattens is true when a part is a reference from a flattened namespace. The context references the parser
// reads back from the vocabulary are not: they round trip without a marker.
func flattens(parts []expr.Part) bool {
	for _, p := range parts {
		if r, ok := p.Expr.(expr.Ref); ok && len(r.Path) > 0 && slices.Contains(flattensRoots, r.Path[0]) {
			return true
		}
	}
	return false
}

func flattensFormat(x expr.Expr) bool {
	c, ok := x.(expr.Call)
	if !ok {
		return false
	}
	parts, ok := emitkit.UnmixFormat(c)
	return ok && flattens(parts)
}

// exprText converts an expression the way exprValue writes a whole value.
func exprText(x expr.Expr) (string, []string, error) {
	if c, ok := x.(expr.Call); ok {
		if parts, ok := emitkit.UnmixFormat(c); ok {
			return convertParts(parts)
		}
	}
	return convertParts([]expr.Part{{Expr: x}})
}

// refMarks returns the marker that carries the pipeline's conversion pairs.
func refMarks(pp string, pl *ir.Pipeline) ([]string, error) {
	pairs := refPairs(pl)
	if len(pairs) == 0 {
		return nil, nil
	}
	arg, err := markerArg(pairs)
	if err != nil {
		return nil, err
	}
	return []string{emitkit.Marker(caps.ScriptInterpolation, pp), arg}, nil
}

// collapseRefs puts the source text back in every line and value that equals a converted text.
func collapseRefs(c *collapseCtx) bool {
	if len(c.args) != 1 {
		return false
	}
	var pairs []refPair
	if json.Unmarshal([]byte(c.args[0]), &pairs) != nil {
		return false
	}
	byG := map[string]refPair{}
	for _, p := range pairs {
		byG[p.G] = p
	}
	line := func(s *string) {
		if p, ok := byG[*s]; ok && p.Kind == pairText {
			*s = p.S
		}
	}
	vars := func(vs []ir.Var) {
		for i := range vs {
			v := &vs[i].Value
			if v.Kind != ir.ValueLit {
				continue
			}
			p, ok := byG[v.Lit]
			switch {
			case !ok:
			case p.Kind == pairText:
				v.Lit = p.S
			case p.Kind == pairExpr:
				if x, err := expr.ParseCanonical(p.S); err == nil {
					*v = ir.Value{Kind: ir.ValueExpr, Expr: x}
				}
			}
		}
	}
	vars(c.pl.Env)
	for _, j := range allJobs(c.pl) {
		vars(j.Env)
		for i := range j.Runner.Labels {
			line(&j.Runner.Labels[i])
		}
		for _, st := range j.Steps {
			vars(st.Env)
			for i := range st.Script {
				line(&st.Script[i])
			}
		}
	}
	return true
}
