// Package gha parses and prints GitHub Actions ${{ }} expressions as canonical expr ASTs.
package gha

import (
	"fmt"
	"strconv"
)

type tokKind uint8

const (
	tEOF tokKind = iota
	tIdent
	tNum
	tStr
	tLParen
	tRParen
	tLBrack
	tRBrack
	tComma
	tDot
	tStar
	tBang
	tNe
	tEq
	tLt
	tLe
	tGt
	tGe
	tAnd
	tOr
)

type token struct {
	text string
	num  float64
	pos  int
	kind tokKind
}

func syntaxErr(pos int, format string, a ...any) error {
	return fmt.Errorf("gha expression: %s at position %d", fmt.Sprintf(format, a...), pos+1)
}

func isIdentStart(c byte) bool { return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isHex(c byte) bool        { return isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'f') }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) || c == '-' }

var punct = map[string]tokKind{
	"(": tLParen, ")": tRParen, "[": tLBrack, "]": tRBrack, ",": tComma, ".": tDot, "*": tStar,
	"!": tBang, "!=": tNe, "==": tEq, "<": tLt, "<=": tLe, ">": tGt, ">=": tGe, "&&": tAnd, "||": tOr,
}

func lex(src string) ([]token, error) {
	var toks []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'':
			s, end, err := lexString(src, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tStr, text: s, pos: i})
			i = end
		case isDigit(c) || (c == '-' && i+1 < len(src) && isDigit(src[i+1])):
			n, end, err := lexNumber(src, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{kind: tNum, num: n, pos: i})
			i = end
		case isIdentStart(c):
			j := i + 1
			for j < len(src) && isIdentPart(src[j]) {
				j++
			}
			toks = append(toks, token{kind: tIdent, text: src[i:j], pos: i})
			i = j
		default:
			k, n := lexPunct(src, i)
			if n == 0 {
				return nil, syntaxErr(i, "unexpected character %q", c)
			}
			toks = append(toks, token{kind: k, pos: i})
			i += n
		}
	}
	return append(toks, token{kind: tEOF, pos: len(src)}), nil
}

func lexPunct(src string, i int) (tokKind, int) {
	if i+1 < len(src) {
		if k, ok := punct[src[i:i+2]]; ok {
			return k, 2
		}
	}
	if k, ok := punct[src[i:i+1]]; ok {
		return k, 1
	}
	return 0, 0
}

func lexString(src string, start int) (string, int, error) {
	var b []byte
	for i := start + 1; i < len(src); i++ {
		if src[i] != '\'' {
			b = append(b, src[i])
			continue
		}
		if i+1 < len(src) && src[i+1] == '\'' {
			b = append(b, '\'')
			i++
			continue
		}
		return string(b), i + 1, nil
	}
	return "", 0, syntaxErr(start, "unterminated string")
}

// lexNumber reads a JSON-style decimal or a 0x hex integer, with an optional leading minus.
func lexNumber(src string, start int) (float64, int, error) {
	i := start
	if src[i] == '-' {
		i++
	}
	hex := i+2 < len(src) && src[i] == '0' && src[i+1]|0x20 == 'x' && isHex(src[i+2])
	var lit string
	if hex {
		j := i + 2
		for j < len(src) && isHex(src[j]) {
			j++
		}
		lit = src[start:j] + "p0"
		i = j
	} else {
		i = scanDigits(src, i)
		if i+1 < len(src) && src[i] == '.' && isDigit(src[i+1]) {
			i = scanDigits(src, i+1)
		}
		if i < len(src) && src[i]|0x20 == 'e' {
			j := i + 1
			if j < len(src) && (src[j] == '+' || src[j] == '-') {
				j++
			}
			if j < len(src) && isDigit(src[j]) {
				i = scanDigits(src, j)
			}
		}
		lit = src[start:i]
	}
	if i < len(src) && isIdentPart(src[i]) {
		return 0, 0, syntaxErr(i, "unexpected character %q after number", src[i])
	}
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil {
		return 0, 0, syntaxErr(start, "number out of range")
	}
	return f, i, nil
}

func scanDigits(src string, i int) int {
	for i < len(src) && isDigit(src[i]) {
		i++
	}
	return i
}
