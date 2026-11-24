package gitlab

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// emitter carries the state of one Emit call: the ledger and the lost-text comments waiting for a node.
type emitter struct {
	led *emitkit.Ledger
	pl  *ir.Pipeline
	// wfGuard is the workflow condition that Parse ANDed into every job; the written workflow block already says it.
	wfGuard expr.Expr
	from    *expr.Dialect
	fold    *folded
	splits  map[*ir.Job]splitPart
	noSplit map[string]string
	deadOut map[string]bool
	// noVar holds the inputs that got no variable, because another input owns the name.
	noVar    map[string]bool
	secrets  map[ir.FeatureUse][]string
	comments []string
}

// sourceDialect is the expression dialect of a project's origin; nil means GitLab semantics already hold.
func sourceDialect(origin string) *expr.Dialect { return expr.SourceFor(origin, exprgitlab.Lang) }

func (e *emitter) use(path string, f caps.Feature) ir.FeatureUse {
	return ir.FeatureUse{IRPath: path, Feature: f}
}

func (e *emitter) lose(path string, f caps.Feature, detail string) {
	e.led.Note(e.use(path, f), caps.Support{Level: caps.Lost}, detail)
}

// foreignExt reports Ext data of other providers: it is not in the GitLab file, only in the round-trip metadata.
func (e *emitter) foreignExt(path string, x ir.Ext) {
	emitkit.ForeignExt(e.led, dialect, "GitLab", path, x)
}

// loseName reports a display name the file cannot hold; only a job name equal to its key is written.
func (e *emitter) loseName(path, name, key string) {
	if name == "" || name == key {
		return
	}
	if key == "" && strings.HasPrefix(path, "pipeline/") && !strings.Contains(path[len("pipeline/"):], "/") && !pipelineName(name) {
		e.lose(path, caps.DisplayName, "the pipeline name cannot be written as workflow:name: it is empty, over "+
			strconv.Itoa(workflowNameMax)+" bytes, or holds a dollar sign that GitLab expands as a variable; the name is kept in round-trip metadata")
		return
	}
	e.lose(path, caps.DisplayName, "GitLab CI has no display name here; the name is kept in round-trip metadata")
}

// workflowNameMax is the longest text GitLab takes for workflow:name.
const workflowNameMax = 255

// pipelineName reports whether name can be written as workflow:name. GitLab expands $VAR in it, so a name
// with a dollar sign or a workflow expression would read differently; it stays in round-trip metadata.
func pipelineName(name string) bool {
	return name != "" && len(name) <= workflowNameMax && !strings.Contains(name, "$")
}

// secretSetup reports that the operator creates the variable of a secret. One entry names every secret of a
// node, because the ledger keeps one entry for each node and feature. A service secret reports under job.env.
func (e *emitter) secretSetup(path string, f caps.Feature, name string) {
	if f == caps.JobServices {
		f = caps.JobEnv // the services entry also holds the ports verdict and would hide the setup
	}
	u := e.use(path, f)
	if e.secrets == nil {
		e.secrets = map[ir.FeatureUse][]string{}
	}
	if !slices.Contains(e.secrets[u], name) {
		e.secrets[u] = append(e.secrets[u], name)
	}
	names := strings.Join(e.secrets[u], ", ")
	e.external(path, f, "create "+names+" as a masked CI/CD variable in the project settings, protected too for protected branches",
		"a GitLab file cannot mark a secret as one; it writes "+names+" as plain variable references and the operator sets the values there")
}

func (e *emitter) emulate(path string, f caps.Feature, strategy, detail string) {
	e.led.Note(e.use(path, f), caps.Support{Level: caps.Emulated, Strategy: strategy}, detail)
}

func (e *emitter) external(path string, f caps.Feature, strategy, detail string) {
	e.led.Note(e.use(path, f), caps.Support{Level: caps.External, Strategy: strategy}, detail)
}

// comment queues a lost-text line for the next node; "${{" never reaches the output.
func (e *emitter) comment(s string) { e.comments = append(e.comments, lostPrefix+commentSafe(s)) }

func (e *emitter) drain() []string {
	c := e.comments
	e.comments = nil
	return c
}

func isIdent(s string) bool { return identRE.MatchString(s) }

// inputVar names the variable that carries a pipeline input. A name with a character that a shell variable
// cannot hold, such as the hyphen of git-url, gets an underscore in its place. The marker of the pipeline keeps
// the input name for the return trip.
func inputVar(name string) string {
	if name == "" {
		return ""
	}
	v := strings.Map(func(r rune) rune {
		if r == '_' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			return r
		}
		return '_'
	}, name)
	if v[0] >= '0' && v[0] <= '9' {
		v = "_" + v
	}
	return v
}

// refVar maps a canonical or gitlab-native ref to the GitLab variable that holds it.
func refVar(path []string) (string, bool) {
	if len(path) == 0 {
		return "", false
	}
	switch path[0] {
	case expr.PrefixInputs:
		if len(path) != 2 || inputVar(path[1]) == "" {
			return "", false
		}
		return inputVar(path[1]), true
	case exprgitlab.Dialect, expr.PrefixEnv, expr.PrefixSecrets, expr.PrefixVars, expr.PrefixMatrix:
		if len(path) != 2 || !isIdent(path[1]) {
			return "", false
		}
		return path[1], true
	case expr.PrefixNeeds:
		ok := len(path) == 4 && path[2] == "outputs" && isIdent(path[3])
		if !ok {
			return "", false
		}
		return path[3], true
	}
	if dp, ok := exprgitlab.Vocab.Dialect(path); ok && len(dp) == 1 {
		return dp[0], true
	}
	return "", false
}

func identPart(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// convert rewrites a canonical template as GitLab text. lost holds the canonical text of each
// expression GitLab has no variable for; those parts are omitted from out.
func convert(s string) (out string, lost []string, err error) {
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return "", nil, err
	}
	return convertParts(parts)
}

func convertParts(parts []expr.Part) (string, []string, error) {
	var sb strings.Builder
	var lost []string
	for i, p := range parts {
		if p.Expr == nil {
			sb.WriteString(p.Text)
			continue
		}
		if l, ok := p.Expr.(expr.Lit); ok {
			sb.WriteString(litText(l))
			continue
		}
		r, ok := foldRef(p.Expr).(expr.Ref)
		name := ""
		if ok {
			name, ok = refVar(r.Path)
		}
		if !ok {
			lost = append(lost, expr.String(p.Expr))
			continue
		}
		next := byte(0)
		if i+1 < len(parts) && parts[i+1].Expr == nil && parts[i+1].Text != "" {
			next = parts[i+1].Text[0]
		}
		if next != 0 && identPart(next) {
			sb.WriteString("${" + name + "}")
		} else {
			sb.WriteString("$" + name)
		}
	}
	return sb.String(), lost, nil
}

func litText(l expr.Lit) string {
	switch l.Kind {
	case expr.LitStr:
		return l.Str
	case expr.LitNull:
		return ""
	}
	return expr.String(l)
}

// refLines lists the GitLab text of each source line whose expressions convert to variables. Collapsing
// rewrites only these lines, so a literal $name in a line the emitter left alone stays literal.
func refLines(src []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range src {
		if !strings.Contains(l, "${{") {
			continue
		}
		if t, _, err := convert(l); err == nil && t != l && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out
}

// refArg is the marker argument that names the lines whose variables came from references.
type refArg struct {
	Lines []string `json:"lines"`
}

// jobRefLines is refLines over a job's runner labels and step scripts.
func jobRefLines(j *ir.Job) []string {
	src := slices.Clone(j.Runner.Labels)
	for _, st := range j.Steps {
		src = append(src, st.Script...)
	}
	return refLines(src)
}

// field converts text that sits inside a YAML value; expressions with no GitLab variable are omitted and reported.
func (e *emitter) field(path string, f caps.Feature, s string) (string, error) {
	if !strings.Contains(s, "${{") {
		return s, nil
	}
	out, lost, err := convert(s)
	if err != nil {
		return "", err
	}
	lost = append(lost, e.noVarRefs(s)...)
	for _, l := range lost {
		e.lose(path, f, "no GitLab variable for expression "+l)
		e.comment(string(f) + " " + path + ": {{ " + l + " }}")
	}
	return out, nil
}

// scriptLine converts one script entry. why is empty when the entry stays; otherwise it names the reason GitLab
// cannot spell the entry, and the caller comments out the whole statement around it, because dropping the part
// could change what the command does.
func (e *emitter) scriptLine(s string) (out, why string, err error) {
	if !strings.Contains(s, "${{") {
		return s, "", nil
	}
	out, lost, err := convert(s)
	if err != nil {
		return "", "", err
	}
	lost = append(lost, e.deadOutputRefs(s)...)
	lost = append(lost, e.noVarRefs(s)...)
	if len(lost) == 0 && strings.Contains(out, rawExprOpen) {
		return "", "the text holds a literal " + rawExprOpen + " that GitLab never expands", nil
	}
	if len(lost) == 0 {
		return out, "", nil
	}
	return "", "no GitLab variable for expression " + strings.Join(lost, ", "), nil
}

// value renders an env value; ok is false when it was lost and must be omitted.
func (e *emitter) value(path string, f caps.Feature, v ir.Value) (text string, ok bool, err error) {
	switch v.Kind {
	case ir.ValueSecret:
		if !isIdent(v.Secret) {
			e.lose(path, caps.ExprOpaque, "secret name "+v.Secret+" is not a GitLab variable name")
			return "", false, nil
		}
		e.secretSetup(path, f, v.Secret)
		return "$" + v.Secret, true, nil
	case ir.ValueExpr:
		return e.exprValue(path, v.Expr)
	}
	t, err := e.field(path, caps.ScriptInterpolation, v.Lit)
	return t, true, err
}

func (e *emitter) exprValue(path string, x expr.Expr) (string, bool, error) {
	if c, ok := x.(expr.Call); ok {
		if parts, ok := emitkit.UnmixFormat(c); ok {
			t, lost, err := convertParts(parts)
			e.reportLost(path, lost)
			return t, err == nil, err
		}
	}
	t, lost, err := convertParts([]expr.Part{{Expr: x}})
	if err != nil {
		return "", false, err
	}
	if len(lost) > 0 {
		e.reportLostExpr(path, lost)
		return "", false, nil
	}
	return t, true, nil
}

func (e *emitter) reportLost(path string, lost []string) {
	for _, l := range lost {
		e.lose(path, caps.ExprOpaque, "no GitLab variable for expression "+l)
		e.comment("expr.opaque " + path + ": " + l)
	}
}

func (e *emitter) reportLostExpr(path string, lost []string) {
	e.reportLost(path, lost)
}

// vars renders a variables mapping in order; extras holds the hash-form keys by variable name.
func (e *emitter) vars(path string, f caps.Feature, vs []ir.Var, extras any) (*yaml.Node, error) {
	return e.varsAt(func(ir.Var) (string, caps.Feature) { return path, f }, vs, extras)
}

// varsAt is vars with a report path and feature chosen per variable.
func (e *emitter) varsAt(at func(ir.Var) (string, caps.Feature), vs []ir.Var, extras any) (*yaml.Node, error) {
	ex, _ := extras.(map[string]any)
	m := newMap()
	for _, v := range vs {
		path, f := at(v)
		t, ok, err := e.value(path, f, v.Value)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		rest, hasRest := ex[v.Name].(map[string]any)
		if !hasRest {
			m.set(v.Name, str(t))
			continue
		}
		hv := newMap()
		hv.set("value", str(t))
		if err := hv.addExtra(rest, nil); err != nil {
			return nil, err
		}
		m.set(v.Name, hv.n)
	}
	return m.n, nil
}

// splitStatus pulls the always, failure and success calls out of an AND chain. ok is false when a status
// function sits anywhere else or cancelled() is used, which GitLab has no job state for.
func splitStatus(x expr.Expr) (rest expr.Expr, status string, ok bool) {
	var conj []expr.Expr
	flatten(x, &conj)
	var kept []expr.Expr
	for _, c := range conj {
		call, isCall := c.(expr.Call)
		if !isCall || len(call.Args) != 0 {
			kept = append(kept, c)
			continue
		}
		switch call.Name {
		case "always", "failure", "success":
			if status != "" && status != call.Name {
				return nil, "", false
			}
			status = call.Name
		case "cancelled":
			return nil, "", false
		default:
			kept = append(kept, c)
		}
	}
	for _, k := range kept {
		if hasStatusCall(k) {
			return nil, "", false
		}
	}
	for _, k := range kept {
		if rest == nil {
			rest = k
			continue
		}
		rest = expr.Binary{Op: expr.OpAnd, L: rest, R: k}
	}
	return rest, status, true
}

func flatten(x expr.Expr, out *[]expr.Expr) {
	if b, ok := x.(expr.Binary); ok && b.Op == expr.OpAnd {
		flatten(b.L, out)
		flatten(b.R, out)
		return
	}
	*out = append(*out, x)
}

func hasStatusCall(x expr.Expr) bool {
	switch v := x.(type) {
	case expr.Call:
		switch v.Name {
		case "always", "failure", "success", "cancelled":
			return true
		}
		for _, a := range v.Args {
			if hasStatusCall(a) {
				return true
			}
		}
	case expr.Unary:
		return hasStatusCall(v.X)
	case expr.Binary:
		return hasStatusCall(v.L) || hasStatusCall(v.R)
	}
	return false
}

// condition translates x from the source dialect and checks GitLab can print it. A condition GitLab
// cannot state is reported and ok is false, so the caller disables the construct.
func (e *emitter) condition(path string, f caps.Feature, x expr.Expr, what string) (expr.Expr, bool) {
	if o, isOpaque := x.(expr.Opaque); isOpaque {
		if o.Dialect == exprgitlab.Dialect {
			return o, true
		}
		e.disable(path, caps.ExprOpaque, x, what)
		return nil, false
	}
	t := x
	if e.from != nil {
		var ok bool
		if t, ok = expr.TranslateFacts(x, *e.from, exprgitlab.Lang, expr.Facts{}); !ok {
			e.disable(path, f, x, what)
			return nil, false
		}
	}
	if _, err := exprgitlab.Print(t); err != nil {
		e.disable(path, f, x, what)
		return nil, false
	}
	return t, true
}

func (e *emitter) disable(path string, f caps.Feature, x expr.Expr, what string) {
	e.lose(path, f, "condition not expressible in GitLab CI; "+what+" disabled")
	e.comment(string(f) + " " + path + ": " + expr.String(x))
}

// cancelLost reports cancel-in-progress, which a resource_group cannot do; the group only queues runs.
func (e *emitter) cancelLost(pp string, c *ir.Concurrency) {
	if c.CancelInProgress {
		e.lose(pp, caps.PipelineConcurrency, "cancel-in-progress has no GitLab form; a resource_group queues runs")
	}
}

// outputText is the dotenv value of a job output. ok is false for an output no dotenv file can carry; why says so.
func (e *emitter) outputText(o ir.Output) (text string, ok bool, why string, err error) {
	if !isIdent(o.Name) {
		return "", false, "output name " + o.Name + " is not a dotenv variable name", nil
	}
	if o.Value == "" {
		return "", false, "output " + o.Name + " has no value expression", nil
	}
	text, lost, err := convert(o.Value)
	if err != nil {
		return "", false, "", err
	}
	if len(lost) > 0 {
		return "", false, "no GitLab variable for expression " + strings.Join(lost, ", "), nil
	}
	if strings.Contains(text, "\n") {
		return "", false, "output " + o.Name + " holds a newline, which dotenv cannot carry", nil
	}
	return text, true, "", nil
}

// markDeadOutputs records the outputs of pl that never reach a dotenv file, so a consumer line that reads one is not emitted.
func (e *emitter) markDeadOutputs(pl *ir.Pipeline) error {
	e.deadOut = map[string]bool{}
	for _, j := range allJobs(pl) {
		for _, o := range j.Outputs {
			_, ok, _, err := e.outputText(o)
			if err != nil {
				return err
			}
			if !ok {
				e.deadOut[j.ID+"."+o.Name] = true
			}
		}
	}
	return nil
}

// dropInput records an input that got no variable.
func (e *emitter) dropInput(name string) {
	if e.noVar == nil {
		e.noVar = map[string]bool{}
	}
	e.noVar[name] = true
}

// noVarRefs lists the references in s to an input that has no variable of its own. The shared variable name
// would read another input.
func (e *emitter) noVarRefs(s string) []string {
	if len(e.noVar) == 0 {
		return nil
	}
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range parts {
		if r, ok := p.Expr.(expr.Ref); ok && len(r.Path) == 2 && r.Path[0] == expr.PrefixInputs && e.noVar[r.Path[1]] {
			out = append(out, expr.String(r))
		}
	}
	return out
}

// deadOutputRefs lists the needs.<job>.outputs.<name> references in s whose producer cannot write them.
func (e *emitter) deadOutputRefs(s string) []string {
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range parts {
		r, ok := p.Expr.(expr.Ref)
		if ok && len(r.Path) == 4 && r.Path[0] == expr.PrefixNeeds && r.Path[2] == "outputs" && e.deadOut[r.Path[1]+"."+r.Path[3]] {
			out = append(out, expr.String(r))
		}
	}
	return out
}

// foldRef reads the ref_type test GHA needs for a branch or tag name back as the canonical branch or tag ref.
func foldRef(x expr.Expr) expr.Expr {
	b, ok := x.(expr.Binary)
	if ok && b.Op == expr.OpOr {
		if l, isLit := b.R.(expr.Lit); isLit && l.Kind == expr.LitStr && l.Str == "" {
			b, ok = b.L.(expr.Binary)
		}
	}
	if !ok || b.Op != expr.OpAnd {
		return x
	}
	name, ok := b.R.(expr.Ref)
	if !ok || strings.Join(name.Path, ".") != expr.CtxRefName {
		return x
	}
	g, ok := b.L.(expr.Binary)
	if !ok || g.Op != expr.OpEq {
		return x
	}
	ref, ok := g.L.(expr.Ref)
	lit, isLit := g.R.(expr.Lit)
	if !ok || !isLit || strings.Join(ref.Path, ".") != expr.CtxRefType {
		return x
	}
	switch lit.Str {
	case "branch":
		return expr.Ref{Path: []string{expr.CtxBranch}}
	case "tag":
		return expr.Ref{Path: []string{expr.CtxTag}}
	}
	return x
}

// flattenedNote reports text whose reference namespaces print the same in GitLab (matrix.x and env.x are both
// $x) at a site refPairs does not record, so only round-trip metadata can restore them.
func (e *emitter) flattenedNote(path string, f caps.Feature, s string) {
	if parts, err := expr.ParseScriptTemplate(s); err == nil && flattens(parts) {
		e.emulate(path, f, "namespace flattened", "namespace flattened; restored only with round-trip metadata")
	}
}
