package vars

import "strings"

// Ref is one variable read in a text. Bare is false when every read of the name has a default or an error
// operator, as in ${NAME:-x}, so the text copes with the name being unset.
type Ref struct {
	Name string
	Bare bool
}

// Refs lists the variable names that text reads, in order of first use, as $NAME, ${NAME} or ${NAME:-x}. With
// shell set, text is shell source: a reference inside single quotes, behind a backslash, in a comment or in a
// quoted here-document does not expand, so it is not listed. Without it, text is a variable value that the
// provider expands everywhere. A doubled dollar is an escape and never starts a reference.
func Refs(text string, shell bool) []string {
	rs := ScanRefs(text, shell)
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

type heredoc struct {
	word   string
	quoted bool
	tabs   bool
}

// Protected marks the bytes of shell text in which no variable expands: single-quoted text, quoted here-document
// bodies and text behind a backslash.
func Protected(text string) []bool {
	mask := make([]bool, len(text))
	scan(text, true, func(int, Ref) {}, mask)
	return mask
}

// ScanRefs is Refs with the read kind of each name.
func ScanRefs(text string, shell bool) []Ref {
	var out []Ref
	idx := map[string]int{}
	scan(text, shell, func(_ int, r Ref) {
		if i, seen := idx[r.Name]; seen {
			out[i].Bare = out[i].Bare || r.Bare
			return
		}
		idx[r.Name] = len(out)
		out = append(out, r)
	}, nil)
	return out
}

func scan(text string, shell bool, found func(int, Ref), mask []bool) (open bool) {
	var single, double bool
	var pending []heredoc
	protect := func(from, to int) {
		for k := from; k < to && k < len(mask); k++ {
			mask[k] = true
		}
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case single:
			if c == '\'' {
				single = false
			}
			protect(i, i+1)
			continue
		case c == '\n' && shell && len(pending) > 0:
			next, done := heredocBodies(text, i+1, pending, found, protect)
			i = next - 1
			pending = nil
			if !done {
				open = true
			}
			continue
		case c == '\\' && shell:
			protect(i, i+2)
			if i+1 >= len(text) {
				open = true
			}
			i++
			continue
		case c == '\'' && shell && !double:
			single = true
			protect(i, i+1)
			continue
		case c == '"' && shell:
			double = !double
			continue
		case c == '<' && shell && !double && strings.HasPrefix(text[i:], "<<<"):
			i += 2
			continue
		case c == '<' && shell && !double && strings.HasPrefix(text[i:], "<<"):
			if h, n := heredocStart(text, i); n > 0 {
				pending = append(pending, h)
				i += n - 1
			}
			continue
		case c == '#' && shell && !double && (i == 0 || text[i-1] == ' ' || text[i-1] == '\t' || text[i-1] == '\n' || text[i-1] == ';'):
			for i < len(text) && text[i] != '\n' {
				i++
			}
			i--
			continue
		case c != '$':
			continue
		}
		if i+1 >= len(text) {
			break
		}
		start := i + 1
		braced := false
		switch text[start] {
		case '$':
			i++
			continue
		case '{':
			braced = true
			start++
		}
		end := start
		for end < len(text) && nameChar(text[end], end == start) {
			end++
		}
		if end > start {
			found(i, Ref{Name: text[start:end], Bare: !braced || bareAfter(text[end:])})
		}
		i = end - 1
	}
	return open || single || (shell && double) || len(pending) > 0
}

// Open reports whether shell text ends inside a quote, a here-document or a line continuation, so the next line
// still belongs to the same shell statement.
func Open(text string) bool {
	return scan(text, true, func(int, Ref) {}, nil)
}

// bareAfter reports whether the text after a braced name leaves the name without a default or error operator.
func bareAfter(rest string) bool {
	if rest == "" {
		return true
	}
	switch rest[0] {
	case '-', '=', '+', '?':
		return false
	case ':':
		if len(rest) > 1 && strings.ContainsRune("-=+?", rune(rest[1])) {
			return false
		}
	}
	return true
}

// heredocStart reads "<<[-]['"]WORD['"]" at text[i:] and returns the delimiter and the length of the operator.
func heredocStart(text string, i int) (heredoc, int) {
	j := i + 2
	h := heredoc{}
	if j < len(text) && text[j] == '-' {
		h.tabs = true
		j++
	}
	for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
		j++
	}
	if j < len(text) && (text[j] == '\'' || text[j] == '"') {
		q := text[j]
		k := strings.IndexByte(text[j+1:], q)
		if k < 0 {
			return h, 0
		}
		h.word, h.quoted = text[j+1:j+1+k], true
		return h, j + 1 + k + 1 - i
	}
	k := j
	for k < len(text) && (nameChar(text[k], false) || text[k] == '-' || text[k] == '.') {
		k++
	}
	if k == j {
		return h, 0
	}
	h.word = text[j:k]
	return h, k - i
}

// heredocBodies reads the bodies that start at text[from:], one for each pending delimiter, scans an unquoted
// body for references and returns the index after the last delimiter line.
func heredocBodies(text string, from int, pending []heredoc, found func(int, Ref), protect func(int, int)) (int, bool) {
	at := from
	done := true
	for _, h := range pending {
		bodyStart := at
		closed := false
		for at < len(text) {
			nl := strings.IndexByte(text[at:], '\n')
			line := text[at:]
			next := len(text)
			if nl >= 0 {
				line, next = text[at:at+nl], at+nl+1
			}
			if h.tabs {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.word {
				closed = true
				break
			}
			at = next
		}
		done = done && closed
		if h.quoted {
			protect(bodyStart, at)
		} else {
			scanBody(text[bodyStart:at], bodyStart, found)
		}
		if nl := strings.IndexByte(text[min(at, len(text)):], '\n'); nl >= 0 {
			at += nl + 1
		} else {
			at = len(text)
		}
	}
	return at, done
}

// scanBody finds the references of an unquoted here-document body, where only a backslash or a doubled dollar
// stops an expansion.
func scanBody(body string, base int, found func(int, Ref)) {
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\\':
			i++
		case '$':
			if i+1 >= len(body) {
				return
			}
			start := i + 1
			braced := false
			switch body[start] {
			case '$':
				i++
				continue
			case '{':
				braced = true
				start++
			}
			end := start
			for end < len(body) && nameChar(body[end], end == start) {
				end++
			}
			if end > start {
				found(base+i, Ref{Name: body[start:end], Bare: !braced || bareAfter(body[end:])})
			}
			i = end - 1
		}
	}
}

func nameChar(c byte, first bool) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (!first && c >= '0' && c <= '9')
}

// Quote break: a variable that the target expands before the shell reads the text, or that the shell expands
// outside the quotes only, leaves a single-quoted region as '"VAR"'.
const (
	QuoteBreakOpen  = `'"`
	QuoteBreakClose = `"'`
)

// Statement carries the shell state from one script line to the next, so a quote that spans lines is known when
// the next line converts.
type Statement struct{ open string }

// Prefix is the text of the open statement that precedes the next line.
func (s *Statement) Prefix() string {
	if s.open == "" {
		return ""
	}
	return s.open + "\n"
}

// Add records the converted text of a line.
func (s *Statement) Add(line string) {
	s.open = strings.TrimSuffix(s.Prefix()+line, "\n")
	if !Open(s.open) {
		s.open = ""
	}
}

// Reset forgets the open statement.
func (s *Statement) Reset() { s.open = "" }

// BreakOut writes ref for a position after before, which follows the lines of ctx: inside single quotes the ref
// leaves the quotes, and a here-document is left alone because no quote works there.
func BreakOut(ctx, before, ref string) string {
	cur := ctx + before
	if strings.Contains(cur, "<<") || !Protected(cur + "$")[len(cur)] {
		return ref
	}
	return QuoteBreakOpen + ref + QuoteBreakClose
}
