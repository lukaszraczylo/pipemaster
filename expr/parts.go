package expr

import (
	"strconv"
	"strings"
)

// MergeText joins adjacent text parts so a bound literal reads as one text.
func MergeText(parts []Part) []Part {
	var out []Part
	for _, p := range parts {
		if n := len(out); n > 0 && p.Expr == nil && out[n-1].Expr == nil {
			out[n-1].Text += p.Text
			continue
		}
		out = append(out, p)
	}
	return out
}

// JoinParts joins parts into one expression: a lone reference stays itself, and text around references
// becomes format('..{0}..', args) with literal braces doubled.
func JoinParts(parts []Part) Expr {
	if len(parts) == 1 && parts[0].Expr != nil {
		return parts[0].Expr
	}
	var pat strings.Builder
	args := []Expr{nil}
	for _, p := range parts {
		if p.Expr == nil {
			pat.WriteString(strings.NewReplacer("{", "{{", "}", "}}").Replace(p.Text))
			continue
		}
		pat.WriteString("{" + strconv.Itoa(len(args)-1) + "}")
		args = append(args, p.Expr)
	}
	args[0] = Lit{Kind: LitStr, Str: pat.String()}
	return Call{Name: "format", Args: args}
}

// RenameNeeds rewrites the job in every needs reference of e.
func RenameNeeds(e Expr, rename func(string) string) Expr {
	switch v := e.(type) {
	case Ref:
		if len(v.Path) > 1 && v.Path[0] == PrefixNeeds {
			p := append([]string(nil), v.Path...)
			p[1] = rename(p[1])
			return Ref{Path: p}
		}
	case Unary:
		return Unary{Op: v.Op, X: RenameNeeds(v.X, rename)}
	case Binary:
		return Binary{Op: v.Op, L: RenameNeeds(v.L, rename), R: RenameNeeds(v.R, rename)}
	case Call:
		args := make([]Expr, len(v.Args))
		for i, a := range v.Args {
			args[i] = RenameNeeds(a, rename)
		}
		return Call{Name: v.Name, Args: args}
	}
	return e
}

// NeedRefs calls add with the job of every needs reference in e.
func NeedRefs(e Expr, add func(string)) {
	switch v := e.(type) {
	case Ref:
		if len(v.Path) > 1 && v.Path[0] == PrefixNeeds {
			add(v.Path[1])
		}
	case Unary:
		NeedRefs(v.X, add)
	case Binary:
		NeedRefs(v.L, add)
		NeedRefs(v.R, add)
	case Call:
		for _, a := range v.Args {
			NeedRefs(a, add)
		}
	}
}
