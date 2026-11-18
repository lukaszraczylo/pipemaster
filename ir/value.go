package ir

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

const opaquePrefix = "opaque("

var opaqueRE = regexp.MustCompile(`(?s)^opaque\(([^)]*)\): (.*)$`)

// exprText renders an expression for YAML; Opaque renders as `opaque(<dialect>): <text>`.
func exprText(e expr.Expr) (string, error) {
	switch v := e.(type) {
	case expr.Opaque:
		if strings.Contains(v.Dialect, ")") {
			return "", fmt.Errorf("opaque dialect %q must not contain ')'", v.Dialect)
		}
		return opaquePrefix + v.Dialect + "): " + v.Text, nil
	default:
		return "", fmt.Errorf("unsupported expression type %T", e)
	}
}

func parseExprText(s string) (expr.Expr, error) {
	m := opaqueRE.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("unrecognised expression %q", s)
	}
	return expr.Opaque{Dialect: m[1], Text: m[2]}, nil
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
