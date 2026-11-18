package ir

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// exprText renders an expression for YAML in the canonical form of expr.String.
func exprText(e expr.Expr) (string, error) {
	if err := expr.Check(e); err != nil {
		return "", err
	}
	if o, ok := e.(expr.Opaque); ok && strings.Contains(o.Dialect, ")") {
		return "", fmt.Errorf("opaque dialect %q must not contain ')'", o.Dialect)
	}
	return expr.String(e), nil
}

func parseExprText(s string) (expr.Expr, error) {
	return expr.ParseCanonical(s)
}

// optExprText renders a possibly nil expression; nil renders as the empty string.
func optExprText(e expr.Expr) (string, error) {
	if e == nil {
		return "", nil
	}
	return exprText(e)
}

func optParseExpr(s string) (expr.Expr, error) {
	if s == "" {
		return nil, nil
	}
	return parseExprText(s)
}
