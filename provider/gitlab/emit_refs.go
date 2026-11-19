package gitlab

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

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

// refPair records that the emitter wrote GitLab text G at position P for the source S. Parse restores S only
// at P and only while the text there still equals G, so edited text stays as the file holds it.
type refPair struct {
	Kind string `json:"k"`
	P    string `json:"p"`
	G    string `json:"g"`
	S    string `json:"s"`
}

// refSite is one place in the pipeline that holds converted text: a string or a whole value.
type refSite struct {
	text  *string
	val   *ir.Value
	after func()
	pos   string
	// key marks a cache key, which GitLab may split into files and prefix.
	key bool
}

// refSites lists every field the GitLab emitter writes through the text conversion, in a fixed order with a
// stable position each. Emit and Parse both walk it, so a position names the same field on both sides.
func refSites(pl *ir.Pipeline) []refSite {
	var out []refSite
	str := func(pos string, s *string) { out = append(out, refSite{pos: pos, text: s}) }
	key := func(pos string, s *string) { out = append(out, refSite{pos: pos, text: s, key: true}) }
	strs := func(pos string, l []string) {
		for i := range l {
			str(pos+"/"+strconv.Itoa(i), &l[i])
		}
	}
	vars := func(pos string, vs []ir.Var) {
		for i := range vs {
			out = append(out, refSite{pos: pos + "/" + strconv.Itoa(i), val: &vs[i].Value})
		}
	}
	if pl.Concurrency != nil {
		str("concurrency", &pl.Concurrency.Group)
	}
	vars("env", pl.Env)
	for _, j := range allJobs(pl) {
		jp := "job/" + j.ID
		vars(jp+"/env", j.Env)
		strs(jp+"/runner", j.Runner.Labels)
		if j.Container != nil {
			str(jp+"/image", &j.Container.Ref)
		}
		for i := range j.Services {
			sp := jp + "/service/" + strconv.Itoa(i)
			str(sp+"/image", &j.Services[i].Image.Ref)
			str(sp+"/name", &j.Services[i].Name)
			vars(sp+"/env", j.Services[i].Env)
		}
		for i := range j.Cache {
			cp := jp + "/cache/" + strconv.Itoa(i)
			key(cp+"/key", &j.Cache[i].Key)
			strs(cp+"/paths", j.Cache[i].Paths)
		}
		for i := range j.Artifacts {
			ap := jp + "/artifact/" + strconv.Itoa(i)
			str(ap+"/name", &j.Artifacts[i].Name)
			strs(ap+"/paths", j.Artifacts[i].Paths)
		}
		if j.Environment != nil {
			str(jp+"/environment/name", &j.Environment.Name)
			str(jp+"/environment/url", &j.Environment.URL)
		}
		if j.Matrix != nil {
			for i := range j.Matrix.Axes {
				strs(jp+"/matrix/"+strconv.Itoa(i), j.Matrix.Axes[i].Values)
			}
			for i := range j.Matrix.Include {
				for k := range j.Matrix.Include[i] {
					str(jp+"/include/"+strconv.Itoa(i)+"/"+strconv.Itoa(k), &j.Matrix.Include[i][k].Value)
				}
			}
		}
		for _, st := range j.Steps {
			sp := jp + "/step/" + st.ID
			vars(sp+"/env", st.Env)
			strs(sp+"/script", st.Script)
			str(sp+"/workdir", &st.WorkDir)
			if st.Image != nil {
				str(sp+"/image", &st.Image.Ref)
			}
			keys := make([]string, 0, len(st.With))
			for k := range st.With {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				v := st.With[k]
				out = append(out, refSite{pos: sp + "/with/" + k, val: &v, after: func() { st.With[k] = v }})
			}
		}
	}
	return out
}

// hashedKeyText is the key text Parse reads back from the key:files form the emitter writes for a hashFiles key.
func hashedKeyText(key string) (string, bool) {
	files, kept, dropped, ok := hashKeyParts(key)
	if !ok || len(dropped) > 0 || len(files) == 0 || len(files) > maxKeyFiles {
		return "", false
	}
	parts, err := expr.ParseScriptTemplate(key)
	if err != nil || !flattens(kept) {
		return "", false
	}
	prefix, lost, err := convertParts(kept)
	if err != nil || len(lost) > 0 {
		return "", false
	}
	prefix = strings.TrimSuffix(prefix, "-")
	canon, err := canonText(prefix)
	if err != nil {
		return "", false
	}
	hash, err := expr.PrintScriptTemplate(parts[len(parts)-1:])
	if err != nil {
		return "", false
	}
	if canon == "" {
		return hash, true
	}
	return canon + "-" + hash, true
}

// refPairs lists every flattening conversion in the pipeline: references such as matrix.x, inputs.x or
// env.x all print as $x, so the namespace cannot be read back from the text alone.
func refPairs(pl *ir.Pipeline) []refPair {
	var out []refPair
	text := func(pos, s string) {
		if parts, err := expr.ParseScriptTemplate(s); err != nil || !flattens(parts) {
			return
		}
		if t, lost, err := convert(s); err == nil && len(lost) == 0 && t != s {
			out = append(out, refPair{pairText, pos, t, s})
		}
	}
	for _, st := range refSites(pl) {
		switch {
		case st.text != nil && st.key:
			if g, ok := hashedKeyText(*st.text); ok {
				out = append(out, refPair{pairText, st.pos, g, *st.text})
				continue
			}
			text(st.pos, *st.text)
		case st.text != nil:
			text(st.pos, *st.text)
		case st.val.Kind == ir.ValueLit:
			text(st.pos, st.val.Lit)
		case st.val.Kind == ir.ValueExpr:
			x := st.val.Expr
			if !flattens([]expr.Part{{Expr: x}}) && !flattensFormat(x) {
				continue
			}
			if t, lost, err := exprText(x); err == nil && len(lost) == 0 {
				out = append(out, refPair{pairExpr, st.pos, t, expr.String(x)})
			}
		}
	}
	return out
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

// collapseRefs puts the source text back at each recorded position whose text still equals what the emitter
// wrote; a position whose text changed keeps the file's text and is reported.
func collapseRefs(c *collapseCtx) bool {
	if len(c.args) != 1 {
		return false
	}
	var pairs []refPair
	if json.Unmarshal([]byte(c.args[0]), &pairs) != nil {
		return false
	}
	at := map[string]refSite{}
	for _, st := range refSites(c.pl) {
		at[st.pos] = st
	}
	for _, p := range pairs {
		st, ok := at[p.P]
		if !ok {
			continue
		}
		cur := ""
		switch {
		case st.text != nil:
			cur = *st.text
		case st.val.Kind == ir.ValueLit:
			cur = st.val.Lit
		}
		if cur != p.G {
			if canon, err := canonText(p.G); err != nil || cur != canon {
				c.ignore("marker ignored: the text at " + p.P + " was edited after conversion, so the file's text stands")
				continue
			}
		}
		switch {
		case st.text != nil:
			*st.text = p.S
		case p.Kind == pairText:
			st.val.Lit = p.S
		default:
			if x, err := expr.ParseCanonical(p.S); err == nil {
				*st.val = ir.Value{Kind: ir.ValueExpr, Expr: x}
			}
		}
		if st.after != nil {
			st.after()
		}
	}
	return true
}
