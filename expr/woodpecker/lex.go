package woodpecker

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type tokKind uint8

const (
	tEOF tokKind = iota
	tIdent
	tStr
	tTrue
	tFalse
	tNil
	tLParen
	tRParen
	tLBrack
	tRBrack
	tComma
	tEq
	tNe
	tAnd
	tOr
	tNot
	tMatches
	tContains
	tStartsWith
	tEndsWith
	tIn
	// tOther is syntax outside the subset: numbers, calls, members, other operators, odd strings.
	tOther
)

type token struct {
	text string
	pos  int
	kind tokKind
}

func syntaxErr(pos int, format string, a ...any) error {
	return fmt.Errorf("woodpecker expression: %s at position %d", fmt.Sprintf(format, a...), pos+1)
}

var words = map[string]tokKind{
	"and": tAnd, "or": tOr, "not": tNot, "in": tIn, "matches": tMatches, "contains": tContains,
	"startsWith": tStartsWith, "endsWith": tEndsWith, "true": tTrue, "false": tFalse, "nil": tNil,
	"let": tOther, "if": tOther, "else": tOther,
}

var punct = []struct {
	text string
	kind tokKind
}{
	{"==", tEq}, {"!=", tNe}, {"&&", tAnd}, {"||", tOr},
	{"(", tLParen}, {")", tRParen}, {"[", tLBrack}, {"]", tRBrack}, {",", tComma}, {"!", tNot},
}

func isIdentStart(c byte) bool { return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) }

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// lex splits src. Only an unterminated string or backtick quote is an error; anything else it cannot place
// becomes tOther so the parser can keep the whole expression as Opaque.
func lex(src string) ([]token, error) {
	var toks []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case isSpace(c):
			i++
		case c == '"' || c == '\'':
			t, n, err := lexString(src, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, t)
			i = n
		case c == '`':
			end := strings.IndexByte(src[i+1:], '`')
			if end < 0 {
				return nil, syntaxErr(i, "unterminated raw string")
			}
			toks = append(toks, token{kind: tOther, pos: i})
			i += end + 2
		case isIdentStart(c):
			j := i
			for j < len(src) && isIdentPart(src[j]) {
				j++
			}
			word := src[i:j]
			if k, ok := words[word]; ok {
				toks = append(toks, token{kind: k, text: word, pos: i})
			} else {
				toks = append(toks, token{kind: tIdent, text: word, pos: i})
			}
			i = j
		default:
			k, n := lexPunct(src[i:])
			if n == 0 {
				k, n = tOther, 1
			}
			toks = append(toks, token{kind: k, pos: i})
			i += n
		}
	}
	return append(toks, token{kind: tEOF, pos: len(src)}), nil
}

func lexPunct(s string) (tokKind, int) {
	for _, p := range punct {
		if strings.HasPrefix(s, p.text) {
			return p.kind, len(p.text)
		}
	}
	return 0, 0
}

// lexString reads a quoted string. The escapes \\ \" \' \n \t \r are decoded; any other escape, a raw
// carriage return or control character, or invalid UTF-8 makes the token tOther because the AST holds the
// decoded text and the printer could not reproduce it.
func lexString(src string, start int) (token, int, error) {
	q := src[start]
	var sb strings.Builder
	odd := false
	for i := start + 1; i < len(src); i++ {
		c := src[i]
		switch {
		case c == q:
			if odd || !utf8.ValidString(sb.String()) {
				return token{kind: tOther, pos: start}, i + 1, nil
			}
			return token{kind: tStr, text: sb.String(), pos: start}, i + 1, nil
		case c == '\\' && i+1 < len(src):
			i++
			if d, ok := unescape(src[i]); ok {
				sb.WriteByte(d)
			} else {
				odd = true
			}
		case c == '\\':
			return token{}, 0, syntaxErr(start, "unterminated string")
		case c < 0x20 && c != '\t' && c != '\n':
			odd = true
		default:
			sb.WriteByte(c)
		}
	}
	return token{}, 0, syntaxErr(start, "unterminated string")
}

func unescape(c byte) (byte, bool) {
	switch c {
	case '\\', '"', '\'':
		return c, true
	case 'n':
		return '\n', true
	case 't':
		return '\t', true
	case 'r':
		return '\r', true
	}
	return 0, false
}
