package concourse

import (
	"strings"
	"unicode"
)

// SanitizeID makes id match the Concourse identifier pattern: lower-case letters, digits, "-", "_" and ".",
// starting with a letter or digit. A run of replaced characters becomes one "-"; a valid id is unchanged.
func (adapter) SanitizeID(id string) string {
	var b strings.Builder
	replaced := false
	for _, r := range id {
		if u := unicode.ToLower(r); validRune(u) {
			if b.Len() == 0 && (u == '-' || u == '_' || u == '.') {
				continue
			}
			b.WriteRune(u)
			replaced = false
			continue
		}
		if b.Len() > 0 && !replaced {
			b.WriteByte('-')
			replaced = true
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

// validRune reports whether r is allowed after the first character of an identifier.
func validRune(r rune) bool {
	return r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') ||
		unicode.In(r, unicode.Ll, unicode.Lt, unicode.Lm, unicode.Lo)
}
