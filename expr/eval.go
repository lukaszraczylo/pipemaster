package expr

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Value is a runtime value: nil (null), bool, float64, string, []any or map[string]any.
type Value = any

// Status is the outcome of the work evaluated so far; it drives success(), failure() and cancelled().
type Status uint8

// Statuses.
const (
	Success Status = iota
	Failure
	Cancelled
)

// Env resolves references and reports the current status.
type Env interface {
	// Lookup returns the value at a canonical path; ok is false when it is unset, which evaluates as null.
	Lookup(path []string) (Value, bool)
	Status() Status
}

// Semantics holds the dialect-specific parts of evaluation.
//
// Comparison rules (zero Semantics):
//   - same type: null==null; bools and numbers by value; strings by bytes; arrays and objects are never equal.
//   - different types: both sides coerce to numbers (null 0, true 1, false 0, string parsed after trimming, ""
//     0, unparsable or composite NaN) and compare numerically; NaN is unequal to everything, itself included.
//   - ordering (< <= > >=) uses the same rules; strings compare bytewise; arrays and objects are never ordered.
//
// StrictTypes disables the coercion: mismatched types are unequal under == and make ordering an error.
// CaseInsensitiveEq folds case in string equality, ordering, contains, startsWith and endsWith.
type Semantics struct {
	// Truthy overrides the default truthiness: null, false, 0, NaN and "" are falsy, everything else truthy.
	Truthy            func(Value) bool
	CaseInsensitiveEq bool
	StrictTypes       bool
}

// ErrOpaque is returned when evaluation reaches an Opaque node.
var ErrOpaque = errors.New("opaque expression cannot be evaluated")

// Eval evaluates e. && and || short-circuit and return an operand value, not a bool.
// A reference the env cannot resolve is null. Function names match exactly; the dialect parser normalises case.
// Supported functions: contains startsWith endsWith format join toJSON fromJSON success failure cancelled always.
func Eval(e Expr, env Env, sem Semantics) (Value, error) {
	ev := evaluator{env: env, sem: sem}
	return ev.eval(e)
}

type evaluator struct {
	env Env
	sem Semantics
}

func (ev evaluator) truthy(v Value) bool {
	if ev.sem.Truthy != nil {
		return ev.sem.Truthy(v)
	}
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

func (ev evaluator) eval(e Expr) (Value, error) {
	switch v := e.(type) {
	case Lit:
		return litValue(v)
	case Ref:
		return ev.ref(v)
	case Unary:
		if v.Op != OpNot {
			return nil, fmt.Errorf("unknown unary operator %q", v.Op)
		}
		x, err := ev.eval(v.X)
		if err != nil {
			return nil, err
		}
		return !ev.truthy(x), nil
	case Binary:
		return ev.binary(v)
	case Call:
		return ev.call(v)
	case Opaque:
		return nil, fmt.Errorf("%w: %s dialect", ErrOpaque, v.Dialect)
	}
	return nil, fmt.Errorf("unknown expression node %T", e)
}

func litValue(l Lit) (Value, error) {
	switch l.Kind {
	case LitNull:
		return nil, nil
	case LitBool:
		return l.Bool, nil
	case LitNum:
		return l.Num, nil
	case LitStr:
		return l.Str, nil
	}
	return nil, fmt.Errorf("unknown literal kind %d", l.Kind)
}

func (ev evaluator) ref(r Ref) (Value, error) {
	if len(r.Path) == 0 {
		return nil, errors.New("reference with empty path")
	}
	if ev.env == nil {
		return nil, fmt.Errorf("reference %q needs an env", strings.Join(r.Path, "."))
	}
	v, ok := ev.env.Lookup(r.Path)
	if !ok {
		return nil, nil
	}
	return v, checkValue(v)
}

func checkValue(v Value) error {
	switch v.(type) {
	case nil, bool, float64, string, []any, map[string]any:
		return nil
	}
	return fmt.Errorf("unsupported value type %T", v)
}

func (ev evaluator) binary(b Binary) (Value, error) {
	l, err := ev.eval(b.L)
	if err != nil {
		return nil, err
	}
	switch b.Op {
	case OpAnd:
		if !ev.truthy(l) {
			return l, nil
		}
		return ev.eval(b.R)
	case OpOr:
		if ev.truthy(l) {
			return l, nil
		}
		return ev.eval(b.R)
	}
	r, err := ev.eval(b.R)
	if err != nil {
		return nil, err
	}
	switch b.Op {
	case OpEq:
		return ev.equal(l, r), nil
	case OpNe:
		return !ev.equal(l, r), nil
	case OpLt, OpLe, OpGt, OpGe:
		return ev.order(b.Op, l, r)
	case OpMatch, OpNoMatch:
		m, err := match(l, r)
		if err != nil {
			return nil, err
		}
		return m == (b.Op == OpMatch), nil
	}
	return nil, fmt.Errorf("unknown binary operator %q", b.Op)
}

func isComposite(v Value) bool {
	switch v.(type) {
	case []any, map[string]any:
		return true
	}
	return false
}

func sameType(a, b Value) bool {
	switch a.(type) {
	case nil:
		return b == nil
	case bool:
		_, ok := b.(bool)
		return ok
	case float64:
		_, ok := b.(float64)
		return ok
	case string:
		_, ok := b.(string)
		return ok
	}
	return false
}

func toNumber(v Value) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case string:
		t := strings.TrimSpace(x)
		if t == "" {
			return 0
		}
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return math.NaN()
		}
		return f
	}
	return math.NaN()
}

func (ev evaluator) fold(s string) string {
	if ev.sem.CaseInsensitiveEq {
		return strings.ToLower(s)
	}
	return s
}

func (ev evaluator) equal(l, r Value) bool {
	if isComposite(l) || isComposite(r) {
		return false
	}
	if sameType(l, r) {
		switch x := l.(type) {
		case nil:
			return true
		case string:
			return ev.fold(x) == ev.fold(r.(string))
		default:
			return l == r
		}
	}
	if ev.sem.StrictTypes {
		return false
	}
	return toNumber(l) == toNumber(r)
}

func (ev evaluator) order(op string, l, r Value) (Value, error) {
	if isComposite(l) || isComposite(r) {
		return false, nil
	}
	if ls, ok := l.(string); ok {
		if rs, ok := r.(string); ok {
			return cmpResult(op, strings.Compare(ev.fold(ls), ev.fold(rs))), nil
		}
	}
	if !sameType(l, r) && ev.sem.StrictTypes {
		return nil, fmt.Errorf("cannot order %T and %T under strict types", l, r)
	}
	a, b := toNumber(l), toNumber(r)
	if math.IsNaN(a) || math.IsNaN(b) {
		return false, nil
	}
	c := 0
	switch {
	case a < b:
		c = -1
	case a > b:
		c = 1
	}
	return cmpResult(op, c), nil
}

func cmpResult(op string, c int) bool {
	switch op {
	case OpLt:
		return c < 0
	case OpLe:
		return c <= 0
	case OpGt:
		return c > 0
	}
	return c >= 0
}

// match treats a null subject as a non-match so an unset variable never errors in `x =~ re`.
func match(l, r Value) (bool, error) {
	pat, ok := r.(string)
	if !ok {
		return false, fmt.Errorf("regex must be a string, got %T", r)
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return false, fmt.Errorf("invalid regex %q: %w", pat, err)
	}
	switch s := l.(type) {
	case nil:
		return false, nil
	case string:
		return re.MatchString(s), nil
	}
	return false, fmt.Errorf("regex subject must be a string, got %T", l)
}

// stringify follows the GitHub-style conversion: null is empty, numbers have no exponent, composites are JSON.
func stringify(v Value) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		return strconv.FormatBool(x)
	case float64:
		switch {
		case math.IsNaN(x):
			return "NaN"
		case math.IsInf(x, 1):
			return "Infinity"
		case math.IsInf(x, -1):
			return "-Infinity"
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		return x
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

type funcSpec struct {
	fn       func(ev evaluator, args []Value) (Value, error)
	min, max int // max < 0 means variadic
}

var funcs map[string]funcSpec

func init() {
	statusFn := func(want func(Status) bool) func(evaluator, []Value) (Value, error) {
		return func(ev evaluator, _ []Value) (Value, error) {
			if ev.env == nil {
				return nil, errors.New("status function needs an env")
			}
			return want(ev.env.Status()), nil
		}
	}
	funcs = map[string]funcSpec{
		"success":    {statusFn(func(s Status) bool { return s == Success }), 0, 0},
		"failure":    {statusFn(func(s Status) bool { return s == Failure }), 0, 0},
		"cancelled":  {statusFn(func(s Status) bool { return s == Cancelled }), 0, 0},
		"always":     {func(evaluator, []Value) (Value, error) { return true, nil }, 0, 0},
		"contains":   {fnContains, 2, 2},
		"startsWith": {func(ev evaluator, a []Value) (Value, error) { return fnAffix(ev, a, strings.HasPrefix), nil }, 2, 2},
		"endsWith":   {func(ev evaluator, a []Value) (Value, error) { return fnAffix(ev, a, strings.HasSuffix), nil }, 2, 2},
		"format":     {fnFormat, 1, -1},
		"join":       {fnJoin, 1, 2},
		"toJSON":     {fnToJSON, 1, 1},
		"fromJSON":   {fnFromJSON, 1, 1},
	}
}

func (ev evaluator) call(c Call) (Value, error) {
	spec, ok := funcs[c.Name]
	if !ok {
		return nil, fmt.Errorf("unknown function %q", c.Name)
	}
	n := len(c.Args)
	if n < spec.min || (spec.max >= 0 && n > spec.max) {
		return nil, fmt.Errorf("function %q: got %d arguments, want %s", c.Name, n, arity(spec))
	}
	args := make([]Value, n)
	for i, a := range c.Args {
		v, err := ev.eval(a)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	return spec.fn(ev, args)
}

func arity(s funcSpec) string {
	switch {
	case s.max < 0:
		return fmt.Sprintf("at least %d", s.min)
	case s.min == s.max:
		return strconv.Itoa(s.min)
	}
	return fmt.Sprintf("%d to %d", s.min, s.max)
}

func fnContains(ev evaluator, a []Value) (Value, error) {
	switch s := a[0].(type) {
	case []any:
		for _, el := range s {
			if ev.equal(el, a[1]) {
				return true, nil
			}
		}
		return false, nil
	case nil, map[string]any:
		return false, nil
	}
	return strings.Contains(ev.fold(stringify(a[0])), ev.fold(stringify(a[1]))), nil
}

func fnAffix(ev evaluator, a []Value, f func(s, affix string) bool) Value {
	if a[0] == nil || isComposite(a[0]) {
		return false
	}
	return f(ev.fold(stringify(a[0])), ev.fold(stringify(a[1])))
}

func fnFormat(_ evaluator, a []Value) (Value, error) {
	f := stringify(a[0])
	var sb strings.Builder
	for i := 0; i < len(f); i++ {
		switch c := f[i]; {
		case c == '{' && i+1 < len(f) && f[i+1] == '{':
			sb.WriteByte('{')
			i++
		case c == '}' && i+1 < len(f) && f[i+1] == '}':
			sb.WriteByte('}')
			i++
		case c == '{':
			end := strings.IndexByte(f[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("format %q: unclosed {", f)
			}
			idx, err := strconv.Atoi(f[i+1 : i+end])
			if err != nil || idx < 0 {
				return nil, fmt.Errorf("format %q: bad placeholder %q", f, f[i:i+end+1])
			}
			if idx+1 >= len(a) {
				return nil, fmt.Errorf("format %q: placeholder %d has no argument", f, idx)
			}
			sb.WriteString(stringify(a[idx+1]))
			i += end
		case c == '}':
			return nil, fmt.Errorf("format %q: unmatched }", f)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String(), nil
}

func fnJoin(_ evaluator, a []Value) (Value, error) {
	sep := ","
	if len(a) == 2 {
		sep = stringify(a[1])
	}
	arr, ok := a[0].([]any)
	if !ok {
		if isComposite(a[0]) {
			return nil, errors.New("join needs an array or a string")
		}
		return stringify(a[0]), nil
	}
	parts := make([]string, len(arr))
	for i, el := range arr {
		parts[i] = stringify(el)
	}
	return strings.Join(parts, sep), nil
}

// fnToJSON is compact so the result is deterministic and has no layout to mirror in the Lean model.
func fnToJSON(_ evaluator, a []Value) (Value, error) {
	b, err := json.Marshal(a[0])
	if err != nil {
		return nil, fmt.Errorf("toJSON: %w", err)
	}
	return string(b), nil
}

func fnFromJSON(_ evaluator, a []Value) (Value, error) {
	s, ok := a[0].(string)
	if !ok {
		return nil, fmt.Errorf("fromJSON needs a string, got %T", a[0])
	}
	var v Value
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("fromJSON: %w", err)
	}
	return v, nil
}
