// Package expr holds the expression AST shared by the IR and the providers.
package expr

// Expr is a pipeline expression in some dialect.
type Expr interface{ exprNode() }

// LitKind selects the populated field of a Lit.
type LitKind uint8

// Literal kinds; the zero value is null.
const (
	LitNull LitKind = iota
	LitBool
	LitNum
	LitStr
)

// Operators. A regex on the right of OpMatch/OpNoMatch is an ordinary string Lit holding the pattern.
const (
	OpEq      = "=="
	OpNe      = "!="
	OpLt      = "<"
	OpLe      = "<="
	OpGt      = ">"
	OpGe      = ">="
	OpAnd     = "&&"
	OpOr      = "||"
	OpMatch   = "=~"
	OpNoMatch = "!~"
	OpNot     = "!"
)

// Lit is a literal; only the field named by Kind may be non-zero, and Num must be finite.
type Lit struct {
	Str  string
	Num  float64
	Kind LitKind
	Bool bool
}

// Ref is a context reference as canonical path segments.
type Ref struct{ Path []string }

// Unary applies Op (only OpNot) to X.
type Unary struct {
	X  Expr
	Op string
}

// Binary applies Op to L and R.
type Binary struct {
	L, R Expr
	Op   string
}

// Call invokes the named function.
type Call struct {
	Name string
	Args []Expr
}

// Opaque is an expression the translator keeps as source text because it has no structured form.
type Opaque struct{ Dialect, Text string }

func (Lit) exprNode()    {}
func (Ref) exprNode()    {}
func (Unary) exprNode()  {}
func (Binary) exprNode() {}
func (Call) exprNode()   {}
func (Opaque) exprNode() {}

// HasOpaque reports whether e contains an Opaque node anywhere.
func HasOpaque(e Expr) bool {
	switch v := e.(type) {
	case Opaque:
		return true
	case Unary:
		return HasOpaque(v.X)
	case Binary:
		return HasOpaque(v.L) || HasOpaque(v.R)
	case Call:
		for _, a := range v.Args {
			if HasOpaque(a) {
				return true
			}
		}
	}
	return false
}
