package emitkit

import (
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// UnmixFormat inverts the GHA parser's mixed(): format('a{0}b{1}', x, y) with sequential placeholders
// and at least two parts is the text a${{ x }}b${{ y }}. Any other format call stays a call.
func UnmixFormat(c expr.Call) ([]expr.Part, bool) {
	if c.Name != "format" || len(c.Args) < 2 {
		return nil, false
	}
	pat, ok := c.Args[0].(expr.Lit)
	if !ok || pat.Kind != expr.LitStr {
		return nil, false
	}
	var parts []expr.Part
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			parts = append(parts, expr.Part{Text: text.String()})
			text.Reset()
		}
	}
	next := 0
	s := pat.Str
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "{{"), strings.HasPrefix(s[i:], "}}"):
			text.WriteByte(s[i])
			i++
		case s[i] == '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 || s[i+1:i+end] != strconv.Itoa(next) || next+1 >= len(c.Args) {
				return nil, false
			}
			flush()
			parts = append(parts, expr.Part{Expr: c.Args[next+1]})
			next++
			i += end
		case s[i] == '}':
			return nil, false
		default:
			text.WriteByte(s[i])
		}
	}
	flush()
	if next != len(c.Args)-1 || len(parts) < 2 {
		return nil, false
	}
	for _, p := range parts {
		if strings.Contains(p.Text, "${{") {
			return nil, false
		}
	}
	return parts, true
}
