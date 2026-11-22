package emitkit

import (
	"regexp"
	"strconv"
	"strings"
)

// Span is a run of script entries, From to To inclusive, that must be commented out together.
type Span struct {
	From, To int
	// Open is true when the statement never closes, so the span runs to the end of the script.
	Open bool
}

var exprTextRE = regexp.MustCompile(`\$\{\{.*?\}\}`)

// CommentSpans groups the entries flagged in lost into whole shell statements. Each entry that is lost pulls in
// every entry of the statement around it (if/fi, case/esac, loops, braces, subshells, here-documents, quotes and
// continued lines), so the entries that stay are valid shell. A statement that never closes runs to the end.
func CommentSpans(lines []string, lost []bool) []Span {
	var out []Span
	sc := &shScanner{cmdPos: true}
	start := 0
	hit := false
	for i, l := range lines {
		sc.entry(exprTextRE.ReplaceAllString(l, "X"))
		hit = hit || (i < len(lost) && lost[i])
		if sc.clean() {
			if hit {
				out = append(out, Span{From: start, To: i})
			}
			start, hit = i+1, false
		}
	}
	if start < len(lines) && hit {
		out = append(out, Span{From: start, To: len(lines) - 1, Open: true})
	}
	return out
}

type heredoc struct {
	delim string
	dash  bool
}

// shScanner is a small shell lexer. It tracks only what decides where a statement ends.
type shScanner struct {
	here    *heredoc
	stack   []string
	pending []heredoc
	quote   byte
	cmdPos  bool
	cont    bool
	opTrail bool
	// needBody is set after a function name until its body starts; fnName after the `function` keyword; fnCand
	// after a command word, which `()` turns into a function name.
	needBody, fnName, fnCand bool
}

const arith = "))"

func (s *shScanner) inArith() bool {
	for _, c := range s.stack {
		if c == arith {
			return true
		}
	}
	return false
}

func (s *shScanner) clean() bool {
	return len(s.stack) == 0 && s.quote == 0 && !s.cont && !s.opTrail && s.here == nil && len(s.pending) == 0 && !s.needBody && !s.fnName
}

func (s *shScanner) top() string {
	if len(s.stack) == 0 {
		return ""
	}
	return s.stack[len(s.stack)-1]
}

func (s *shScanner) push(closer string) { s.stack = append(s.stack, closer) }

// pop closes the innermost open construct of that kind; a closer with no opener is ignored.
func (s *shScanner) pop(closer string) {
	for i := len(s.stack) - 1; i >= 0; i-- {
		if s.stack[i] == closer {
			s.stack = s.stack[:i]
			return
		}
	}
}

func (s *shScanner) entry(text string) {
	for _, line := range strings.Split(text, "\n") {
		s.line(line)
	}
}

func (s *shScanner) line(line string) {
	if s.here != nil {
		if s.here.dash {
			line = strings.TrimLeft(line, "\t")
		}
		if line == s.here.delim {
			s.here = nil
			s.nextHere()
		}
		return
	}
	s.cont = false
	s.scan(line)
	if s.quote == 0 && !s.cont {
		s.cmdPos = true
	}
	s.nextHere()
}

func (s *shScanner) nextHere() {
	if s.here == nil && len(s.pending) > 0 {
		h := s.pending[0]
		s.pending = s.pending[1:]
		s.here = &h
	}
}

func (s *shScanner) scan(line string) {
	var word strings.Builder
	plain := true
	flush := func() {
		if word.Len() == 0 && plain {
			return
		}
		s.keyword(word.String(), plain)
		word.Reset()
		plain = true
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		if s.quote != 0 {
			i = s.inQuote(line, i)
			continue
		}
		if c != ' ' && c != '\t' {
			s.opTrail = false
		}
		switch {
		case c == ' ' || c == '\t':
			flush()
		case c == '\\':
			plain = false
			if i == len(line)-1 {
				s.cont = true
			}
			i++
		case c == '\'' || c == '"' || c == '`':
			plain = false
			s.quote = c
		case c == '#' && word.Len() == 0 && plain:
			return
		case c == '$' && strings.HasPrefix(line[i:], "$(("):
			plain = false
			s.push(arith)
			s.cmdPos = false
			i += 2
		case c == '$' && i+1 < len(line) && line[i+1] == '(':
			plain = false
			s.push(")")
			s.cmdPos = true
			i++
		case c == '(':
			flush()
			i = s.openParen(line, i)
		case c == ')':
			flush()
			i = s.closeParen(line, i)
		case c == ';' || c == '&' || c == '|':
			flush()
			s.cmdPos = true
			if (c == '&' || c == '|') && i+1 < len(line) && line[i+1] == c {
				i++
				s.opTrail = true
			} else if c == '|' {
				s.opTrail = true
			}
		case c == '<' && !s.inArith() && strings.HasPrefix(line[i:], "<<") && !strings.HasPrefix(line[i:], "<<<"):
			flush()
			i = s.heredocOp(line, i+2)
		default:
			word.WriteByte(c)
		}
	}
	flush()
}

// openParen handles a `(` and returns the index of the last byte it consumed. `f()` and `function f()` define a
// function, so its body must follow; `((` at a command position opens arithmetic.
func (s *shScanner) openParen(line string, i int) int {
	rest := strings.TrimLeft(line[i+1:], " \t")
	if strings.HasPrefix(rest, ")") && (s.fnCand || s.needBody) {
		s.needBody, s.fnCand, s.cmdPos = true, false, true
		return len(line) - len(rest)
	}
	s.needBody = false
	switch {
	case s.cmdPos && strings.HasPrefix(line[i:], "(("):
		s.push(arith)
		s.cmdPos = false
		return i + 1
	case s.top() != "esac":
		s.push(")")
	}
	s.cmdPos = true
	return i
}

func (s *shScanner) closeParen(line string, i int) int {
	switch s.top() {
	case arith:
		if i+1 < len(line) && line[i+1] == ')' {
			s.pop(arith)
			i++
		}
		s.cmdPos = false
	case ")":
		s.pop(")")
		s.cmdPos = false
	default:
		s.cmdPos = s.top() == "esac"
	}
	return i
}

// inQuote advances over quoted text from line[i] and returns the index of the last consumed byte.
func (s *shScanner) inQuote(line string, i int) int {
	for ; i < len(line); i++ {
		switch {
		case line[i] == s.quote:
			s.quote = 0
			return i
		case line[i] == '\\' && s.quote == '"' && i+1 < len(line):
			i++
		}
	}
	return i
}

// heredocOp reads the delimiter after << and returns the index of its last byte.
func (s *shScanner) heredocOp(line string, i int) int {
	h := heredoc{}
	if i < len(line) && line[i] == '-' {
		h.dash = true
		i++
	}
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	var d strings.Builder
	for ; i < len(line); i++ {
		c := line[i]
		if c == '\'' || c == '"' || c == '\\' {
			continue
		}
		if strings.IndexByte(" \t;&|()<>", c) >= 0 {
			break
		}
		d.WriteByte(c)
	}
	if d.Len() > 0 {
		h.delim = d.String()
		s.pending = append(s.pending, h)
	}
	s.cmdPos = false
	return i - 1
}

func (s *shScanner) keyword(w string, plain bool) {
	if s.fnName {
		s.fnName, s.needBody, s.cmdPos = false, true, true
		return
	}
	s.needBody = false
	if !plain || !s.cmdPos {
		s.cmdPos = false
		return
	}
	switch w {
	case "if":
		s.push("fi")
	case "while", "until":
		s.push("done")
	case "for":
		s.push("done")
		s.cmdPos = false
	case "case":
		s.push("esac")
		s.cmdPos = false
	case "{":
		s.push("}")
	case "fi", "done", "esac", "}":
		s.pop(w)
		s.cmdPos = false
	case "function":
		s.fnName = true
		s.cmdPos = false
	case "then", "do", "else", "elif", "!", "time":
	default:
		s.cmdPos = false
		s.fnCand = true
	}
}

// SpanDetail words the report entry for one commented-out span: why is the cause of the first lost entry and n
// the number of entries the span holds.
func SpanDetail(why string, n int, open bool) string {
	switch {
	case open:
		return why + "; the statement never closes, so it and the rest of the script are commented out"
	case n > 1:
		return why + "; the whole statement of " + strconv.Itoa(n) + " lines is commented out so the shell stays valid"
	}
	return why + "; the line is commented out"
}

// DropLost comments out the whole statement around every entry of texts whose why is not empty and returns the
// indexes of the entries that stay. report runs once per commented span with the report detail and the span text.
func DropLost(texts, why []string, report func(detail, text string)) []int {
	lost := make([]bool, len(texts))
	for i, w := range why {
		lost[i] = w != ""
	}
	drop := make([]bool, len(texts))
	for _, sp := range CommentSpans(texts, lost) {
		first := ""
		for i := sp.From; i <= sp.To; i++ {
			drop[i] = true
			if first == "" {
				first = why[i]
			}
		}
		report(SpanDetail(first, sp.To-sp.From+1, sp.Open), strings.Join(texts[sp.From:sp.To+1], "\n"))
	}
	var keep []int
	for i := range texts {
		if !drop[i] {
			keep = append(keep, i)
		}
	}
	return keep
}
