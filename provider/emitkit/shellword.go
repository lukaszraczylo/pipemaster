package emitkit

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

const safeWord = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"

// ShellWord writes the parts of one command word as one shell word. A word that holds a reference is double
// quoted so its value stays one word; a plain word is single quoted unless it is made of safe characters.
func ShellWord(parts []expr.Part) (string, error) {
	if hasRef(parts) {
		quoted := make([]expr.Part, len(parts))
		copy(quoted, parts)
		for i, p := range quoted {
			if p.Expr == nil {
				quoted[i].Text = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(p.Text)
			}
		}
		t, err := expr.PrintScriptTemplate(quoted)
		return `"` + t + `"`, err
	}
	var lit strings.Builder
	for _, p := range parts {
		lit.WriteString(p.Text)
	}
	s := lit.String()
	if s != "" && strings.Trim(s, safeWord) == "" {
		return expr.PrintScriptTemplate([]expr.Part{{Text: s}})
	}
	return expr.PrintScriptTemplate([]expr.Part{{Text: "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"}})
}

func hasRef(parts []expr.Part) bool {
	for _, p := range parts {
		if p.Expr != nil {
			return true
		}
	}
	return false
}
