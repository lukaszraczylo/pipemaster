package gha

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Part is one piece of a template; it is shared with the canonical script templates.
type Part = expr.Part

// ParseTemplate splits s into text and ${{ }} parts. Empty text is dropped; a string with no
// ${{ yields one text part. An unterminated ${{ is an error.
func ParseTemplate(s string) ([]Part, error) {
	parts, err := expr.SplitTemplate(s, Parse)
	if err != nil {
		return nil, fmt.Errorf("gha template: %w", err)
	}
	return parts, nil
}

// PrintTemplate inverts ParseTemplate; each expression prints as `${{ <expr> }}`, so
// PrintTemplate(ParseTemplate(s)) equals s up to whitespace and spelling inside the delimiters.
func PrintTemplate(parts []Part) (string, error) {
	var sb strings.Builder
	for i, p := range parts {
		switch {
		case p.Expr == nil:
			if strings.Contains(p.Text, "${{") {
				return "", fmt.Errorf("gha template: part %d text contains %q", i, "${{")
			}
			sb.WriteString(p.Text)
		case p.Text != "":
			return "", fmt.Errorf("gha template: part %d has both Expr and Text", i)
		default:
			s, err := Print(p.Expr)
			if err != nil {
				return "", fmt.Errorf("gha template: part %d: %w", i, err)
			}
			sb.WriteString("${{ " + s + " }}")
		}
	}
	return sb.String(), nil
}
