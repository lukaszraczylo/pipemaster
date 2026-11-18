// Package expr holds the expression AST shared by the IR and the providers.
package expr

// Expr is a pipeline expression in some dialect.
type Expr interface{ exprNode() }

// Opaque is an expression the translator keeps as source text because it has no structured form.
type Opaque struct{ Dialect, Text string }

func (Opaque) exprNode() {}
