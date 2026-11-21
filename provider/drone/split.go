package drone

import "strings"

const (
	separator  = "---"
	terminator = "..."
)

// chunk is one YAML document of the file and the line where its text starts.
type chunk struct {
	text  string
	start int
}

// splitDocuments cuts the file the way Drone does before it parses YAML: a line that starts with "---" ends the
// document, and a line that starts with "..." ends the file (research drone.md 2.1). Blank documents are dropped.
func splitDocuments(data string) []chunk {
	var out []chunk
	var cur []string
	start := 1
	flush := func(next int) {
		if text := strings.Join(cur, "\n"); strings.TrimSpace(text) != "" {
			out = append(out, chunk{text: text, start: start})
		}
		cur, start = nil, next
	}
	for i, line := range strings.Split(data, "\n") {
		switch {
		case strings.HasPrefix(line, terminator):
			flush(i + 2)
			return out
		case strings.HasPrefix(line, separator):
			flush(i + 2)
		default:
			cur = append(cur, line)
		}
	}
	flush(0)
	return out
}
