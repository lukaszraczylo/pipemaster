package argo_test

import (
	"github.com/lukaszraczylo/pipemaster/expr"
)

func ref(path ...string) expr.Expr { return expr.Ref{Path: path} }

func str(s string) expr.Expr { return expr.Lit{Kind: expr.LitStr, Str: s} }

func num(n float64) expr.Expr { return expr.Lit{Kind: expr.LitNum, Num: n} }

func boolean(b bool) expr.Expr { return expr.Lit{Kind: expr.LitBool, Bool: b} }

func bin(op string, l, r expr.Expr) expr.Expr { return expr.Binary{Op: op, L: l, R: r} }

func not(x expr.Expr) expr.Expr { return expr.Unary{Op: expr.OpNot, X: x} }

func and(l, r expr.Expr) expr.Expr { return bin(expr.OpAnd, l, r) }

func or(l, r expr.Expr) expr.Expr { return bin(expr.OpOr, l, r) }

func eq(name, lit string) expr.Expr { return bin(expr.OpEq, ref(name), str(lit)) }

func ne(name, lit string) expr.Expr { return bin(expr.OpNe, ref(name), str(lit)) }
