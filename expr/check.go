package expr

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// MaxDepth bounds expression nesting in every parser so hostile input cannot exhaust the stack.
const MaxDepth = 50

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// ReservedCallName is the Call name no expression may use; `opaque(` starts the top-level Opaque form.
const ReservedCallName = "opaque"

// IsIdent reports whether s is a canonical identifier: a letter or underscore, then letters, digits, '_' or '-'.
func IsIdent(s string) bool { return identRE.MatchString(s) }

// IsIdentStart reports whether c can begin an identifier.
func IsIdentStart(c byte) bool { return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }

// IsIdentPart reports whether c can continue an identifier.
func IsIdentPart(c byte) bool { return IsIdentStart(c) || c == '-' || (c >= '0' && c <= '9') }

// QuoteSingle wraps s in single quotes, doubling embedded quotes.
func QuoteSingle(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

var validBinOps = map[string]bool{
	OpEq: true, OpNe: true, OpLt: true, OpLe: true, OpGt: true, OpGe: true,
	OpAnd: true, OpOr: true, OpMatch: true, OpNoMatch: true,
}

// Check reports why e has no canonical text that ParseCanonical reads back exactly:
// a nil node, a non-finite number, an unknown literal kind or operator, a literal with a field its kind does not use,
// a Call name that is not an identifier, an empty Ref path, or nesting deeper than MaxDepth.
// Nesting is one unit per '!', per call, and per parenthesised level: a Binary operand that is itself Binary prints in parens,
// except the left operand of a same-operator && or || chain.
func Check(e Expr) error {
	d, err := check(e)
	if err != nil {
		return err
	}
	if d > MaxDepth {
		return fmt.Errorf("expression nested deeper than %d", MaxDepth)
	}
	return nil
}

// operandDepth is the depth of e as an operand of a binary operator or '!'.
func operandDepth(e Expr) (int, error) {
	d, err := check(e)
	if _, ok := e.(Binary); ok {
		d++
	}
	return d, err
}

// check returns the nesting depth of e. It stops descending below MaxDepth+1 only through the callers' comparison,
// so a hostile AST costs its own size, not more.
func check(e Expr) (int, error) {
	switch v := e.(type) {
	case nil:
		return 0, fmt.Errorf("nil expression")
	case Lit:
		return 0, checkLit(v)
	case Ref:
		if len(v.Path) == 0 {
			return 0, fmt.Errorf("reference with empty path")
		}
	case Unary:
		if v.Op != OpNot {
			return 0, fmt.Errorf("unknown unary operator %q", v.Op)
		}
		d, err := operandDepth(v.X)
		return d + 1, err
	case Binary:
		if !validBinOps[v.Op] {
			return 0, fmt.Errorf("unknown binary operator %q", v.Op)
		}
		var l int
		var err error
		if flatChain(v.Op, v.L) {
			l, err = check(v.L)
		} else {
			l, err = operandDepth(v.L)
		}
		if err != nil {
			return 0, err
		}
		r, err := operandDepth(v.R)
		return max(l, r), err
	case Call:
		if !IsIdent(v.Name) || v.Name == ReservedCallName {
			return 0, fmt.Errorf("call name %q is not an identifier or is reserved", v.Name)
		}
		deepest := 0
		for _, a := range v.Args {
			d, err := check(a)
			if err != nil {
				return 0, err
			}
			deepest = max(deepest, d)
		}
		return deepest + 1, nil
	case Opaque:
	default:
		return 0, fmt.Errorf("unknown expression node %T", e)
	}
	return 0, nil
}

func checkLit(l Lit) error {
	stray := func(ok bool) error {
		if ok {
			return fmt.Errorf("literal kind %d carries a field it does not use", l.Kind)
		}
		return nil
	}
	switch l.Kind {
	case LitNull:
		return stray(l.Str != "" || l.Num != 0 || l.Bool)
	case LitBool:
		return stray(l.Str != "" || l.Num != 0)
	case LitNum:
		if math.IsNaN(l.Num) || math.IsInf(l.Num, 0) {
			return fmt.Errorf("non-finite number has no canonical text")
		}
		return stray(l.Str != "" || l.Bool)
	case LitStr:
		return stray(l.Num != 0 || l.Bool)
	}
	return fmt.Errorf("unknown literal kind %d", l.Kind)
}
