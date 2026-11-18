package gitlab

import (
	"fmt"
	"strings"
)

type tokKind uint8

const (
	tEOF tokKind = iota
	tVar
	tStr
	tPattern
	tNull
	tBool
	tInput
	tLParen
	tRParen
	tEq
	tNe
	tMatch
	tNoMatch
	tAnd
	tOr
	tNot
)

type token struct {
	text  string // variable name, string body, pattern body, or input source text
	flags string // pattern flags
	pos   int
	kind  tokKind
}

func syntaxErr(pos int, format string, a ...any) error {
	return fmt.Errorf("gitlab expression: %s at position %d", fmt.Sprintf(format, a...), pos+1)
}

func isWord(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

var punct = []struct {
	text string
	kind tokKind
}{
	{"==", tEq}, {"!=", tNe}, {"=~", tMatch}, {"!~", tNoMatch}, {"&&", tAnd}, {"||", tOr},
	{"(", tLParen}, {")", tRParen}, {"!", tNot},
}

func lex(src string) ([]token, error) {
	var toks []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case isSpace(c):
			i++
		case c == '"' || c == '\'':
			end := strings.IndexByte(src[i+1:], c)
			if end < 0 {
				return nil, syntaxErr(i, "unterminated string")
			}
			toks = append(toks, token{kind: tStr, text: src[i+1 : i+1+end], pos: i})
			i += end + 2
		case c == '/':
			t, n, err := lexPattern(src, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, t)
			i = n
		case c == '$' && strings.HasPrefix(src[i:], "$[["):
			end := strings.Index(src[i:], "]]")
			if end < 0 {
				return nil, syntaxErr(i, "unterminated $[[ ]] input")
			}
			toks = append(toks, token{kind: tInput, text: src[i : i+end+2], pos: i})
			i += end + 2
		case c == '$':
			j := i + 1
			for j < len(src) && isWord(src[j]) {
				j++
			}
			if j == i+1 {
				return nil, syntaxErr(i, "expected a variable name after '$'")
			}
			toks = append(toks, token{kind: tVar, text: src[i+1 : j], pos: i})
			i = j
		case isWord(c):
			j := i
			for j < len(src) && isWord(src[j]) {
				j++
			}
			switch word := src[i:j]; word {
			case "null":
				toks = append(toks, token{kind: tNull, pos: i})
			case "true", "false":
				toks = append(toks, token{kind: tBool, text: word, pos: i})
			default:
				return nil, syntaxErr(i, "unknown lexeme %q", word)
			}
			i = j
		default:
			k, n := lexPunct(src[i:])
			if n == 0 {
				return nil, syntaxErr(i, "unknown lexeme %q", c)
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

// lexPattern reads /body/flags. GitLab's pattern needs at least two body characters, the last not a
// backslash, and `\/` stands for a literal slash.
func lexPattern(src string, start int) (token, int, error) {
	var body []byte
	i := start + 1
	for ; i < len(src); i++ {
		if src[i] == '\\' && i+1 < len(src) && src[i+1] == '/' {
			body = append(body, '/')
			i++
			continue
		}
		if src[i] == '/' {
			break
		}
		body = append(body, src[i])
	}
	if i >= len(src) {
		return token{}, 0, syntaxErr(start, "unterminated regular expression")
	}
	if len(body) < 2 || body[len(body)-1] == '\\' {
		return token{}, 0, syntaxErr(start, "invalid regular expression")
	}
	j := i + 1
	for j < len(src) && strings.IndexByte("ismU", src[j]) >= 0 {
		j++
	}
	return token{kind: tPattern, text: string(body), flags: src[i+1 : j], pos: start}, j, nil
}
