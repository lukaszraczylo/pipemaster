package woodpecker

import "strings"

const fallbackID = "workflow"

// SanitizeID makes id a workflow file name: Woodpecker names a workflow by its file name without leading dots
// and extension, so the id keeps letters, digits, underscore, dash and dot, with no leading dot.
func (adapter) SanitizeID(id string) string {
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !idByte(c) {
			c = '-'
		}
		out = append(out, c)
	}
	s := strings.TrimLeft(string(out), ".")
	if s == "" {
		return fallbackID
	}
	return s
}

func idByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}
