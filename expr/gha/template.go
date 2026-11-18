package gha

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

const (
	tmplOpen  = "${{"
	tmplClose = "}}"
)

// Part is one piece of a template: literal Text, or Expr (Text empty) for a ${{ }} section.
type Part struct {
	Expr expr.Expr
	Text string
}

// ParseTemplate splits s into text and ${{ }} parts. Empty text is dropped; a string with no
// ${{ yields one text part. An unterminated ${{ is an error.
func ParseTemplate(s string) ([]Part, error) {
	var parts []Part
	for len(s) > 0 {
		open := strings.Index(s, tmplOpen)
		if open < 0 {
			return append(parts, Part{Text: s}), nil
		}
		if open > 0 {
			parts = append(parts, Part{Text: s[:open]})
		}
		body := s[open+len(tmplOpen):]
		end := closeIndex(body)
		if end < 0 {
			return nil, fmt.Errorf("gha template: unterminated %q", tmplOpen)
		}
		e, err := Parse(body[:end])
		if err != nil {
			return nil, fmt.Errorf("gha template: %w", err)
		}
		parts = append(parts, Part{Expr: e})
		s = body[end+len(tmplClose):]
	}
	return parts, nil
}

// closeIndex finds the first "}}" outside a single-quoted string, or -1.
func closeIndex(body string) int {
	inStr := false
	for i := 0; i < len(body); i++ {
		switch {
		case body[i] == '\'':
			inStr = !inStr
		case !inStr && strings.HasPrefix(body[i:], tmplClose):
			return i
		}
	}
	return -1
}

// PrintTemplate inverts ParseTemplate; each expression prints as `${{ <expr> }}`, so
// PrintTemplate(ParseTemplate(s)) equals s up to whitespace and spelling inside the delimiters.
func PrintTemplate(parts []Part) (string, error) {
	var sb strings.Builder
	for i, p := range parts {
		switch {
		case p.Expr == nil:
			if strings.Contains(p.Text, tmplOpen) {
				return "", fmt.Errorf("gha template: part %d text contains %q", i, tmplOpen)
			}
			sb.WriteString(p.Text)
		case p.Text != "":
			return "", fmt.Errorf("gha template: part %d has both Expr and Text", i)
		default:
			s, err := Print(p.Expr)
			if err != nil {
				return "", fmt.Errorf("gha template: part %d: %w", i, err)
			}
			sb.WriteString(tmplOpen + " " + s + " " + tmplClose)
		}
	}
	return sb.String(), nil
}
