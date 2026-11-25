package expr

import "strings"

type tri uint8

const (
	triFalse tri = iota
	triTrue
	triUnknown
)

// EventsPossible returns the events of the list on which e can hold. It reads only the tests of the event ref
// (== != =~ !~ with a string or an exact, prefix, suffix or substring pattern) under !, && and ||, and treats every
// other test as unknown, so an event is dropped only when e is false on it whatever the other refs say.
func EventsPossible(e Expr, events []string) []string {
	var out []string
	for _, ev := range events {
		if eventTri(e, ev) != triFalse {
			out = append(out, ev)
		}
	}
	return out
}

func eventTri(e Expr, ev string) tri {
	switch v := e.(type) {
	case Unary:
		if v.Op != OpNot {
			return triUnknown
		}
		switch eventTri(v.X, ev) {
		case triTrue:
			return triFalse
		case triFalse:
			return triTrue
		}
	case Binary:
		return eventTriBinary(v, ev)
	}
	return triUnknown
}

func eventTriBinary(b Binary, ev string) tri {
	switch b.Op {
	case OpAnd:
		l, r := eventTri(b.L, ev), eventTri(b.R, ev)
		switch {
		case l == triFalse || r == triFalse:
			return triFalse
		case l == triTrue && r == triTrue:
			return triTrue
		}
	case OpOr:
		l, r := eventTri(b.L, ev), eventTri(b.R, ev)
		switch {
		case l == triTrue || r == triTrue:
			return triTrue
		case l == triFalse && r == triFalse:
			return triFalse
		}
	case OpEq, OpNe:
		x, lit, ok := refLit(b.L, b.R)
		if !ok || !isRef(x, CtxEvent) {
			return triUnknown
		}
		return triOf((lit == ev) == (b.Op == OpEq))
	case OpMatch, OpNoMatch:
		s, ok := isStr(b.R)
		if !ok || !isRef(b.L, CtxEvent) {
			return triUnknown
		}
		m, lit, pok := ParsePattern(s)
		if !pok {
			return triUnknown
		}
		return triOf(modeHolds(m, asciiLower(ev), asciiLower(lit)) == (b.Op == OpMatch))
	}
	return triUnknown
}

func triOf(b bool) tri {
	if b {
		return triTrue
	}
	return triFalse
}

func modeHolds(m Mode, s, lit string) bool {
	switch m {
	case ModeExact:
		return s == lit
	case ModePrefix:
		return strings.HasPrefix(s, lit)
	case ModeSuffix:
		return strings.HasSuffix(s, lit)
	}
	return strings.Contains(s, lit)
}
