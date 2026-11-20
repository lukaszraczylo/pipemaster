package expr

// TranslateRefs moves a value expression between dialects. It accepts only references, string literals and
// format calls over them, because the canonical name of a reference stands for one value in every dialect that
// names it. Both vocabularies must name every reference, and the event reference is refused: its values differ by dialect.
func TranslateRefs(e Expr, from, to Dialect) (Expr, bool) {
	if from.Class == 0 || to.Class == 0 || !refsOnly(e) || !from.names(e) || !to.names(e) {
		return nil, false
	}
	if to.Supports != nil && !to.Supports(e) {
		return nil, false
	}
	return e, true
}

func refsOnly(e Expr) bool {
	switch v := e.(type) {
	case Lit:
		return v.Kind == LitStr
	case Ref:
		return len(v.Path) > 0 && (len(v.Path) > 1 || v.Path[0] != CtxEvent)
	case Call:
		if v.Name != "format" {
			return false
		}
		for _, a := range v.Args {
			if !refsOnly(a) {
				return false
			}
		}
		return true
	}
	return false
}
