package argo

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lukaszraczylo/pipemaster/expr"
)

type kind uint8

const (
	kLParen kind = iota + 1
	kRParen
	kNot
	kEq
	kNe
	kAnd
	kOr
	kStr
	kNum
	kBool
	kRef
)

type token struct {
	str  string
	ref  expr.Ref
	num  float64
	kind kind
	bool bool
	// bare marks a govaluate word with no quotes, which is a string only on the right of a comparison.
	bare bool
}

type lexer struct {
	s     string
	toks  []token
	i     int
	scope Scope
	gov   bool
}

// lex reads the text of one `when`. An expression body (gov false) is the expr-lang subset; the govaluate
// form (gov true) is the same operators over {{simple}} tags and bare words.
func lex(s string, scope Scope, gov bool) ([]token, bool) {
	l := &lexer{s: s, scope: scope, gov: gov}
	for {
		l.skipSpace()
		if l.i >= len(l.s) {
			return l.toks, len(l.toks) > 0
		}
		if !l.step() {
			return nil, false
		}
	}
}

func (l *lexer) rest() string { return l.s[l.i:] }

func (l *lexer) skipSpace() {
	for l.i < len(l.s) && (l.s[l.i] == ' ' || l.s[l.i] == '\t' || l.s[l.i] == '\n' || l.s[l.i] == '\r') {
		l.i++
	}
}

func (l *lexer) push(k kind, width int) bool {
	l.toks = append(l.toks, token{kind: k})
	l.i += width
	return true
}

func (l *lexer) step() bool {
	r := l.rest()
	switch {
	case r[0] == '(':
		return l.push(kLParen, 1)
	case r[0] == ')':
		return l.push(kRParen, 1)
	case strings.HasPrefix(r, "!="):
		return l.push(kNe, 2)
	case r[0] == '!':
		return l.push(kNot, 1)
	case strings.HasPrefix(r, "=="):
		return l.push(kEq, 2)
	case strings.HasPrefix(r, "&&"):
		return l.push(kAnd, 2)
	case strings.HasPrefix(r, "||"):
		return l.push(kOr, 2)
	case r[0] == '\'' || r[0] == '"':
		return l.quoted()
	case l.gov:
		return l.govWord()
	case r[0] >= '0' && r[0] <= '9':
		return l.number()
	}
	return l.chain()
}

func (l *lexer) quoted() bool {
	str, n, ok := scanString(l.rest(), l.gov)
	if !ok {
		return false
	}
	l.toks = append(l.toks, token{kind: kStr, str: str})
	l.i += n
	return true
}

// scanString reads the quoted string at the start of s and gives its value and its width. The govaluate
// form takes no escapes.
func scanString(s string, gov bool) (string, int, bool) {
	q := s[0]
	for j := 1; j < len(s); j++ {
		switch s[j] {
		case q:
			if gov {
				return s[1:j], j + 1, true
			}
			v, ok := unescape(s[1:j], q)
			return v, j + 1, ok
		case '\\':
			if gov {
				return "", 0, false
			}
			j++
		case '\n':
			return "", 0, false
		}
	}
	return "", 0, false
}

var simpleEscapes = map[byte]byte{'a': '\a', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v', '\\': '\\'}

// unescape reads the escapes of an expr-lang string: the simple ones, the quote that encloses the string,
// and \x, octal, \u and \U with a code below the first non-ASCII byte or a valid code point.
func unescape(body string, quote byte) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(body) {
			return "", false
		}
		e := body[i]
		if v, ok := simpleEscapes[e]; ok {
			b.WriteByte(v)
			continue
		}
		start, n, base := i+1, 0, 16
		switch {
		case e == quote:
			b.WriteByte(e)
			continue
		case e >= '0' && e <= '7':
			start, n, base = i, 3, 8
		case e == 'x':
			n = 2
		case e == 'u':
			n = 4
		case e == 'U':
			n = 8
		default:
			return "", false
		}
		if start+n > len(body) {
			return "", false
		}
		code, err := strconv.ParseUint(body[start:start+n], base, 32)
		if err != nil {
			return "", false
		}
		i = start + n - 1
		if e != 'u' && e != 'U' {
			if code >= utf8.RuneSelf {
				return "", false
			}
			b.WriteByte(byte(code))
			continue
		}
		r := rune(code) //nolint:gosec // code is at most 8 hex digits, and ValidRune rejects the rest
		if !utf8.ValidRune(r) {
			return "", false
		}
		b.WriteRune(r)
	}
	return b.String(), true
}

func (l *lexer) number() bool {
	j := l.i
	for j < len(l.s) && isDigit(l.s[j]) {
		j++
	}
	if j < len(l.s) && l.s[j] == '.' {
		k := j + 1
		for k < len(l.s) && isDigit(l.s[k]) {
			k++
		}
		if k == j+1 {
			return false
		}
		j = k
	}
	if j < len(l.s) && (isIdent(l.s[j]) || l.s[j] == '.') {
		return false
	}
	n, err := strconv.ParseFloat(l.s[l.i:j], 64)
	if err != nil {
		return false
	}
	l.toks = append(l.toks, token{kind: kNum, num: n})
	l.i = j
	return true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdent(c byte) bool {
	return c == '_' || isDigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func (l *lexer) ident() string {
	j := l.i
	if j >= len(l.s) || isDigit(l.s[j]) {
		return ""
	}
	for j < len(l.s) && isIdent(l.s[j]) {
		j++
	}
	id := l.s[l.i:j]
	l.i = j
	return id
}

var keywords = map[string]kind{"and": kAnd, "or": kOr, "not": kNot}

// chain reads a keyword, a boolean or a member path such as workflow.parameters['ctx-branch'].
func (l *lexer) chain() bool {
	first := l.ident()
	if first == "" {
		return false
	}
	seg := []string{first}
	for l.i < len(l.s) {
		switch l.s[l.i] {
		case '.':
			l.i++
			id := l.ident()
			if id == "" {
				return false
			}
			seg = append(seg, id)
			continue
		case '[':
			if l.i+1 >= len(l.s) || l.s[l.i+1] != '\'' && l.s[l.i+1] != '"' {
				return false
			}
			name, n, ok := scanString(l.s[l.i+1:], false)
			if !ok || l.i+1+n >= len(l.s) || l.s[l.i+1+n] != ']' {
				return false
			}
			seg = append(seg, name)
			l.i += n + 2
			continue
		}
		break
	}
	if len(seg) == 1 {
		switch first {
		case "true", "false":
			l.toks = append(l.toks, token{kind: kBool, bool: first == "true"})
			return true
		}
		if k, ok := keywords[first]; ok {
			return l.push(k, 0)
		}
	}
	r, ok := fromSegments(seg, l.scope)
	if !ok {
		return false
	}
	return l.pushRef(r)
}

// pushRef adds a ref, and folds the group (needs.x ?? ”) into the ref: an output of a skipped task is absent,
// and the default makes the group read the same as the ref.
func (l *lexer) pushRef(r expr.Ref) bool {
	l.skipSpace()
	if !strings.HasPrefix(l.rest(), "??") {
		l.toks = append(l.toks, token{kind: kRef, ref: r})
		return true
	}
	n := len(l.toks)
	if n == 0 || l.toks[n-1].kind != kLParen || r.Path[0] != expr.PrefixNeeds {
		return false
	}
	l.i += 2
	l.skipSpace()
	if l.i >= len(l.s) || l.s[l.i] != '\'' && l.s[l.i] != '"' {
		return false
	}
	v, w, ok := scanString(l.rest(), false)
	if !ok || v != "" {
		return false
	}
	l.i += w
	l.skipSpace()
	if l.i >= len(l.s) || l.s[l.i] != ')' {
		return false
	}
	l.i++
	l.toks = append(l.toks[:n-1], token{kind: kRef, ref: r})
	return true
}

// govWord reads a {{simple}} tag or a bare word.
func (l *lexer) govWord() bool {
	r := l.rest()
	if strings.HasPrefix(r, tagOpen) {
		end := strings.Index(r, tagClose)
		if end < 0 {
			return false
		}
		body := r[len(tagOpen):end]
		if strings.HasPrefix(strings.TrimSpace(body), "=") {
			return false
		}
		ref, ok := fromTagBody(body, l.scope)
		if !ok {
			return false
		}
		l.toks = append(l.toks, token{kind: kRef, ref: ref})
		l.i += end + len(tagClose)
		return true
	}
	j := 0
	for j < len(r) && isBareWord(r[j], j == 0) {
		j++
	}
	if j == 0 || govKeywords[r[:j]] {
		return false
	}
	l.toks = append(l.toks, token{kind: kStr, str: r[:j], bare: true})
	l.i += j
	return true
}

// govKeywords are the words that govaluate reads as a boolean or an operator, not as a variable.
var govKeywords = map[string]bool{"true": true, "false": true, "in": true, "IN": true}

// isBareWord admits the characters of a govaluate variable token: a letter, then letters, digits and `_`
// (Knetic/govaluate 7625b7f8c03d parsing.go:142-144 and isVariableName at :455-461). Argo's shouldExecute
// retypes that token to a string. Anything else reads as an operator, an accessor or a number there, so a
// word that holds one is left as Opaque.
func isBareWord(c byte, first bool) bool {
	if first {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	return isIdent(c)
}
