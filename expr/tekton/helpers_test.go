package tekton_test

import (
	"github.com/lukaszraczylo/pipemaster/expr"
)

func ref(path ...string) expr.Expr { return expr.Ref{Path: path} }

func str(s string) expr.Expr { return expr.Lit{Kind: expr.LitStr, Str: s} }

func bin(op string, l, r expr.Expr) expr.Expr { return expr.Binary{Op: op, L: l, R: r} }

func eq(name, lit string) expr.Expr { return bin(expr.OpEq, ref(name), str(lit)) }

func ne(name, lit string) expr.Expr { return bin(expr.OpNe, ref(name), str(lit)) }
