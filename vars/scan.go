package vars

// Refs lists the variable names that text reads, in order of first use, as $NAME, ${NAME} or ${NAME:-x}. With
// shell set, text is shell source: a reference inside single quotes, behind a backslash or in a comment does
// not expand, so it is not listed. Without it, text is a variable value that the provider expands everywhere.
// A doubled dollar is an escape and never starts a reference.
func Refs(text string, shell bool) []string {
	var out []string
	seen := map[string]bool{}
	var single, double bool
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case single:
			if c == '\'' {
				single = false
			}
			continue
		case c == '\\' && shell:
			i++
			continue
		case c == '\'' && shell && !double:
			single = true
			continue
		case c == '"' && shell:
			double = !double
			continue
		case c == '#' && shell && !double && (i == 0 || text[i-1] == ' ' || text[i-1] == '\t' || text[i-1] == '\n' || text[i-1] == ';'):
			for i < len(text) && text[i] != '\n' {
				i++
			}
			continue
		case c != '$':
			continue
		}
		if i+1 >= len(text) {
			break
		}
		start := i + 1
		switch text[start] {
		case '$':
			i++
			continue
		case '{':
			start++
		}
		end := start
		for end < len(text) && nameChar(text[end], end == start) {
			end++
		}
		if end > start {
			if n := text[start:end]; !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
		i = end - 1
	}
	return out
}

func nameChar(c byte, first bool) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (!first && c >= '0' && c <= '9')
}
