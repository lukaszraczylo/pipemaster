package expr

import (
	"fmt"
	"strings"
)

const (
	tmplOpen  = "${{"
	tmplClose = "}}"
)

// escapedOpen is how literal text "${{" is written inside a template.
var escapedOpen = tmplOpen + " " + QuoteSingle(tmplOpen) + " " + tmplClose

// Part is one piece of a template: literal Text, or Expr (Text empty) for a ${{ }} section.
type Part struct {
	Expr Expr
	Text string
}

// SplitTemplate splits s into text and ${{ }} parts, handing each body to parse. Empty text is
// dropped; a string with no ${{ yields one text part. An unterminated ${{ is an error.
func SplitTemplate(s string, parse func(body string) (Expr, error)) ([]Part, error) {
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
			return nil, fmt.Errorf("unterminated %q", tmplOpen)
		}
		e, err := parse(body[:end])
		if err != nil {
			return nil, err
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

// ParseScriptTemplate splits a script line into text and canonical ${{ }} parts. Text "${{" appears
// only as the escape `${{ '${{' }}`, which parses to a string Lit.
func ParseScriptTemplate(s string) ([]Part, error) {
	parts, err := SplitTemplate(s, func(body string) (Expr, error) { return ParseCanonical(strings.TrimSpace(body)) })
	if err != nil {
		return nil, fmt.Errorf("script template: %w", err)
	}
	return parts, nil
}

// PrintScriptTemplate inverts ParseScriptTemplate. Literal "${{" in text, even one split across
// adjacent text parts, is written in escaped form so the result parses back to the same text.
func PrintScriptTemplate(parts []Part) (string, error) {
	var sb strings.Builder
	var text strings.Builder
	flush := func() {
		sb.WriteString(strings.ReplaceAll(text.String(), tmplOpen, escapedOpen))
		text.Reset()
	}
	for i, p := range parts {
		switch {
		case p.Expr == nil:
			text.WriteString(p.Text)
		case p.Text != "":
			return "", fmt.Errorf("script template: part %d has both Expr and Text", i)
		default:
			if err := Check(p.Expr); err != nil {
				return "", fmt.Errorf("script template: part %d: %w", i, err)
			}
			flush()
			sb.WriteString(tmplOpen + " " + String(p.Expr) + " " + tmplClose)
		}
	}
	flush()
	return sb.String(), nil
}
