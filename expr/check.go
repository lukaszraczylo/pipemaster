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
// a Call name that is not an identifier, or an empty Ref path.
func Check(e Expr) error {
	switch v := e.(type) {
	case nil:
		return fmt.Errorf("nil expression")
	case Lit:
		return checkLit(v)
	case Ref:
		if len(v.Path) == 0 {
			return fmt.Errorf("reference with empty path")
		}
	case Unary:
		if v.Op != OpNot {
			return fmt.Errorf("unknown unary operator %q", v.Op)
		}
		return Check(v.X)
	case Binary:
		if !validBinOps[v.Op] {
			return fmt.Errorf("unknown binary operator %q", v.Op)
		}
		if err := Check(v.L); err != nil {
			return err
		}
		return Check(v.R)
	case Call:
		if !IsIdent(v.Name) {
			return fmt.Errorf("call name %q is not an identifier", v.Name)
		}
		for _, a := range v.Args {
			if err := Check(a); err != nil {
				return err
			}
		}
	case Opaque:
	default:
		return fmt.Errorf("unknown expression node %T", e)
	}
	return nil
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
