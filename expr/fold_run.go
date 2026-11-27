package expr

import "strings"

const (
	branchRefPrefix = "refs/heads/"
	tagRefPrefix    = "refs/tags/"
	fnStartsWith    = "startsWith"
)

// FoldRun substitutes what a run started by one known event knows and folds the result, so a condition on the
// event, the ref type or the ref keeps only the tests that still depend on the run. A branch push (tag false)
// has event push, ref type branch and no tag. A tag push (tag true) has event tag, ref type tag and no branch.
// Pull request values are null, because the pipeline has no pull request run. A Lit true or Lit false result
// means the condition holds on every such run or on none.
func FoldRun(e Expr, tag bool) Expr {
	return foldConst(substRun(e, tag))
}

// FoldRunAs is FoldRun for a source whose event vocabulary is known. A source with no tag event (tagEvent false:
// GHA, GitLab) reports a tag push as the push event on a tag ref, so on a tag run the event is push, not tag.
func FoldRunAs(e Expr, tag, tagEvent bool) Expr {
	if tag && !tagEvent {
		e, _ = rebuild(e, func(x Expr) (Expr, bool) {
			if isRef(x, CtxEvent) {
				return strLit(runEvent(false)), true
			}
			return nil, false
		})
	}
	return FoldRun(e, tag)
}

func substRun(e Expr, tag bool) Expr {
	switch v := e.(type) {
	case Ref:
		return runRef(v, tag)
	case Unary:
		return Unary{Op: v.Op, X: substRun(v.X, tag)}
	case Binary:
		if x, ok := refEquality(v, tag); ok {
			return x
		}
		return Binary{Op: v.Op, L: substRun(v.L, tag), R: substRun(v.R, tag)}
	case Call:
		if x, ok := refPrefix(v, tag); ok {
			return x
		}
		args := make([]Expr, len(v.Args))
		for i, a := range v.Args {
			args[i] = substRun(a, tag)
		}
		return Call{Name: v.Name, Args: args}
	}
	return e
}

func runRef(r Ref, tag bool) Expr {
	switch name := strings.Join(r.Path, "."); name {
	case CtxEvent:
		return strLit(runEvent(tag))
	case CtxRefType:
		return strLit(runRefType(tag))
	case CtxRefName:
		return Ref{Path: []string{runNameRef(tag)}}
	case CtxPRNumber, CtxPRHeadRef, CtxPRBaseRef, CtxPRBaseSHA:
		return Lit{}
	case CtxTag:
		if !tag {
			return Lit{}
		}
	case CtxBranch:
		if tag {
			return Lit{}
		}
	}
	return r
}

func runEvent(tag bool) string {
	if tag {
		return "tag"
	}
	return "push"
}

func runRefType(tag bool) string {
	if tag {
		return "tag"
	}
	return "branch"
}

func runNameRef(tag bool) string {
	if tag {
		return CtxTag
	}
	return CtxBranch
}

func runRefPrefix(tag bool) string {
	if tag {
		return tagRefPrefix
	}
	return branchRefPrefix
}

// refEquality rewrites a test of the full ref against a string: the ref of the run is the prefix of its type
// followed by the branch or tag name.
func refEquality(b Binary, tag bool) (Expr, bool) {
	if b.Op != OpEq && b.Op != OpNe {
		return nil, false
	}
	x, lit, ok := refLit(b.L, b.R)
	if !ok || !isRef(x, CtxRef) {
		return nil, false
	}
	var out Expr = Lit{Kind: LitBool}
	if rest, has := strings.CutPrefix(lit, runRefPrefix(tag)); has {
		out = Binary{Op: OpEq, L: Ref{Path: []string{runNameRef(tag)}}, R: strLit(rest)}
	}
	if b.Op == OpNe {
		out = Unary{Op: OpNot, X: out}
	}
	return out, true
}

// refPrefix rewrites startsWith on the full ref.
func refPrefix(c Call, tag bool) (Expr, bool) {
	if c.Name != fnStartsWith || len(c.Args) != 2 || !isRef(c.Args[0], CtxRef) {
		return nil, false
	}
	lit, ok := isStr(c.Args[1])
	if !ok {
		return nil, false
	}
	prefix := runRefPrefix(tag)
	if rest, has := strings.CutPrefix(lit, prefix); has {
		if rest == "" {
			return Lit{Kind: LitBool, Bool: true}, true
		}
		return Call{Name: fnStartsWith, Args: []Expr{Ref{Path: []string{runNameRef(tag)}}, strLit(rest)}}, true
	}
	return Lit{Kind: LitBool, Bool: strings.HasPrefix(prefix, lit)}, true
}

// FoldConst folds the literal parts of e: a test of two literals, and an && or || with a literal side.
func FoldConst(e Expr) Expr { return foldConst(e) }

func foldConst(e Expr) Expr {
	switch v := e.(type) {
	case Unary:
		x := foldConst(v.X)
		if l, ok := x.(Lit); ok {
			return Lit{Kind: LitBool, Bool: !LitTruthy(l)}
		}
		return Unary{Op: v.Op, X: x}
	case Binary:
		return foldBinary(v.Op, foldConst(v.L), foldConst(v.R))
	case Call:
		args := make([]Expr, len(v.Args))
		allLit := true
		for i, a := range v.Args {
			args[i] = foldConst(a)
			_, isLit := args[i].(Lit)
			allLit = allLit && isLit
		}
		c := Call{Name: v.Name, Args: args}
		if allLit && (c.Name == fnStartsWith || c.Name == "endsWith" || c.Name == "contains") {
			if val, err := Eval(c, nil, Semantics{}); err == nil {
				if b, ok := val.(bool); ok {
					return Lit{Kind: LitBool, Bool: b}
				}
			}
		}
		return c
	}
	return e
}

func foldBinary(op string, l, r Expr) Expr {
	ll, lok := l.(Lit)
	rl, rok := r.(Lit)
	switch op {
	case OpAnd:
		switch {
		case lok && !LitTruthy(ll), rok && !LitTruthy(rl):
			return Lit{Kind: LitBool}
		case lok:
			return r
		case rok:
			return l
		}
	case OpOr:
		switch {
		case lok && LitTruthy(ll), rok && LitTruthy(rl):
			return Lit{Kind: LitBool, Bool: true}
		case lok:
			return r
		case rok:
			return l
		}
	default:
		if lok && rok {
			if val, err := Eval(Binary{Op: op, L: ll, R: rl}, nil, Semantics{}); err == nil {
				if b, ok := val.(bool); ok {
					return Lit{Kind: LitBool, Bool: b}
				}
			}
		}
	}
	return Binary{Op: op, L: l, R: r}
}

// LitTruthy reports whether a literal holds as a condition: null, false, 0 and the empty string do not.
func LitTruthy(l Lit) bool {
	switch l.Kind {
	case LitBool:
		return l.Bool
	case LitNum:
		return l.Num != 0
	case LitStr:
		return l.Str != ""
	}
	return false
}
