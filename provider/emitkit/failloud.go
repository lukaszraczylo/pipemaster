package emitkit

import (
	"strings"
	"unicode"

	"github.com/lukaszraczylo/pipemaster/caps"
)

const (
	failLoudHead = "printf '%s\\n' 'pipemaster: lost: "
	failLoudTail = "' >&2; exit 1"
	failLoudMax  = 240
)

// FailLoud returns the one-line shell script that stands in for source work with no target form. It prints the
// loss to stderr and exits 1, so a converted pipeline fails where the source would have run the work. The
// text is single-quoted and stripped of characters that a shell or a CI template engine reads.
func FailLoud(f caps.Feature, irPath, reason string) string {
	return failLoudHead + failLoudText(string(f)+" "+irPath+": "+reason) + failLoudTail
}

// IsFailLoud reports whether script is exactly the single line FailLoud writes.
func IsFailLoud(script []string) bool {
	return len(script) == 1 && IsFailLoudLine(script[0])
}

// IsFailLoudLine reports whether line is a FailLoud script.
func IsFailLoudLine(line string) bool {
	return strings.HasPrefix(line, failLoudHead) && strings.HasSuffix(line, failLoudTail) &&
		!strings.Contains(strings.TrimSuffix(strings.TrimPrefix(line, failLoudHead), failLoudTail), "'")
}

func failLoudText(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == failLoudMax {
			break
		}
		switch {
		case r == '$' || r == '{' || r == '}' || r == '`' || r == '\'' || r == '\\' || r == '"' || r == '<' || r == '>':
			r = '?'
		case !unicode.IsPrint(r):
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	return strings.ReplaceAll(b.String(), "((", "( (")
}
