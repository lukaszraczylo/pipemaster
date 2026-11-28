package emitkit

import "strings"

// GlobERE converts a GitLab glob (Ruby File.fnmatch with FNM_PATHNAME, FNM_DOTMATCH and FNM_EXTGLOB) into an
// extended regular expression over whole paths. ok is false for a glob the converter does not model.
func GlobERE(g string) (string, bool) {
	var sb strings.Builder
	braces := 0
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case !GlobByte(c):
			return "", false
		case c == '*' && strings.HasPrefix(g[i:], "**/") && (i == 0 || g[i-1] == '/'):
			sb.WriteString("(.*/)?")
			i += 2
		case c == '*':
			for i+1 < len(g) && g[i+1] == '*' {
				i++
			}
			sb.WriteString("[^/]*")
		case c == '?':
			sb.WriteString("[^/]")
		case c == '{':
			braces++
			sb.WriteString("(")
		case c == '}' && braces > 0:
			braces--
			sb.WriteString(")")
		case c == ',' && braces > 0:
			sb.WriteString("|")
		case c == '[':
			j := strings.IndexByte(g[i:], ']')
			if j < 2 {
				return "", false
			}
			cls, ok := ClassERE(g[i+1 : i+j])
			if !ok {
				return "", false
			}
			sb.WriteString(cls)
			i += j
		case strings.IndexByte(`.+()|^}`, c) >= 0:
			sb.WriteString(`\` + string(c))
		default:
			sb.WriteByte(c)
		}
	}
	if braces != 0 {
		return "", false
	}
	return sb.String(), true
}

// GlobByte is the allow-list of glob characters: printable ASCII without quotes, backslash, dollar and backtick.
// The regex travels as data, never as shell text; the list keeps the markers and the report readable too.
func GlobByte(c byte) bool {
	return c >= 0x20 && c < 0x7f && strings.IndexByte("'\"\\$`", c) < 0
}

// ClassERE converts the body of a glob character class. A class never matches a slash, as under FNM_PATHNAME.
// Only letters, digits, underscore, dot and a range of two alphanumerics are allowed; anything else is not modelled.
func ClassERE(body string) (string, bool) {
	neg := false
	if strings.HasPrefix(body, "!") || strings.HasPrefix(body, "^") {
		neg, body = true, body[1:]
	}
	if body == "" {
		return "", false
	}
	alnum := func(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
	for i := 0; i < len(body); i++ {
		switch c := body[i]; {
		case alnum(c) || c == '_' || c == '.':
		case c == '-' && i > 0 && i+1 < len(body) && alnum(body[i-1]) && alnum(body[i+1]) && body[i-1] <= body[i+1]:
		default:
			return "", false
		}
	}
	if neg {
		return "[^/" + body + "]", true
	}
	return "[" + body + "]", true
}

// AtomERE joins the expressions of several globs into one anchored expression over whole paths.
func AtomERE(globs []string) (string, bool) {
	parts := make([]string, len(globs))
	for i, g := range globs {
		re, ok := GlobERE(g)
		if !ok {
			return "", false
		}
		parts[i] = re
	}
	return "^(" + strings.Join(parts, "|") + ")$", true
}
