//go:build behave

package behave

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// celEval evaluates the small CEL subset that the converters write into EventListener filters and overlays:
// string literals (also raw r'...'), body paths, replace/startsWith/endsWith/matches, string(int(x)), +, ==, !=, &&, || and "in [list]".
// It returns an error for anything else, so the caller can record the event as blocked instead of guessing.
func celEval(expr string, body map[string]any) (any, error) {
	p := &celParser{src: expr, body: body}
	v, err := p.or()
	if err != nil {
		return nil, fmt.Errorf("cel %q: %w", expr, err)
	}
	p.skip()
	if p.pos != len(p.src) {
		return nil, fmt.Errorf("cel %q: unexpected %q", expr, p.src[p.pos:])
	}
	return v, nil
}

type celParser struct {
	body map[string]any
	src  string
	pos  int
}

func (p *celParser) skip() {
	for p.pos < len(p.src) && p.src[p.pos] == ' ' {
		p.pos++
	}
}

func (p *celParser) eat(tok string) bool {
	p.skip()
	if strings.HasPrefix(p.src[p.pos:], tok) {
		p.pos += len(tok)
		return true
	}
	return false
}

func truthy(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func (p *celParser) or() (any, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.eat("||") {
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = truthy(l) || truthy(r)
	}
	return l, nil
}

func (p *celParser) and() (any, error) {
	l, err := p.cmp()
	if err != nil {
		return nil, err
	}
	for p.eat("&&") {
		r, err := p.cmp()
		if err != nil {
			return nil, err
		}
		l = truthy(l) && truthy(r)
	}
	return l, nil
}

func (p *celParser) cmp() (any, error) {
	l, err := p.add()
	if err != nil {
		return nil, err
	}
	switch {
	case p.eat("=="):
		r, err := p.add()
		return err == nil && fmt.Sprint(l) == fmt.Sprint(r) && l != nil, err
	case p.eat("!="):
		r, err := p.add()
		return err == nil && fmt.Sprint(l) != fmt.Sprint(r), err
	case p.eat(" in ") || p.eat("in "):
		r, err := p.add()
		if err != nil {
			return nil, err
		}
		list, _ := r.([]any)
		for _, x := range list {
			if fmt.Sprint(x) == fmt.Sprint(l) {
				return true, nil
			}
		}
		return false, nil
	}
	return l, nil
}

func (p *celParser) add() (any, error) {
	l, err := p.postfix()
	if err != nil {
		return nil, err
	}
	for p.eat("+") {
		r, err := p.postfix()
		if err != nil {
			return nil, err
		}
		l = fmt.Sprint(l) + fmt.Sprint(r)
	}
	return l, nil
}

func (p *celParser) postfix() (any, error) {
	v, err := p.primary()
	if err != nil {
		return nil, err
	}
	for {
		p.skip()
		if p.pos >= len(p.src) || p.src[p.pos] != '.' {
			return v, nil
		}
		p.pos++
		name := p.ident()
		if !p.eat("(") {
			return nil, fmt.Errorf("field %q after a value is unsupported", name)
		}
		args, err := p.args()
		if err != nil {
			return nil, err
		}
		s, _ := v.(string)
		switch name {
		case "replace":
			if len(args) != 3 {
				return nil, fmt.Errorf("replace takes three arguments")
			}
			n, _ := strconv.Atoi(fmt.Sprint(args[2]))
			v = strings.Replace(s, fmt.Sprint(args[0]), fmt.Sprint(args[1]), n)
		case "startsWith":
			v = len(args) == 1 && strings.HasPrefix(s, fmt.Sprint(args[0]))
		case "matches":
			if len(args) != 1 {
				return nil, fmt.Errorf("matches takes one argument")
			}
			re, rerr := regexp.Compile(fmt.Sprint(args[0]))
			if rerr != nil {
				return nil, fmt.Errorf("matches: %w", rerr)
			}
			v = re.MatchString(s)
		case "endsWith":
			v = len(args) == 1 && strings.HasSuffix(s, fmt.Sprint(args[0]))
		default:
			return nil, fmt.Errorf("method %q is unsupported", name)
		}
	}
}

func (p *celParser) args() ([]any, error) {
	var out []any
	if p.eat(")") {
		return out, nil
	}
	for {
		v, err := p.or()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if p.eat(")") {
			return out, nil
		}
		if !p.eat(",") {
			return nil, fmt.Errorf("expected , or )")
		}
	}
}

func (p *celParser) ident() string {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			p.pos++
			continue
		}
		break
	}
	return p.src[start:p.pos]
}

func (p *celParser) primary() (any, error) {
	p.skip()
	if p.pos >= len(p.src) {
		return nil, fmt.Errorf("unexpected end")
	}
	switch c := p.src[p.pos]; {
	case c == 'r' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '\'':
		// A raw string keeps its backslashes, which a regular expression needs.
		end := strings.IndexByte(p.src[p.pos+2:], '\'')
		if end < 0 {
			return nil, fmt.Errorf("unterminated string")
		}
		s := p.src[p.pos+2 : p.pos+2+end]
		p.pos += end + 3
		return s, nil
	case c == '\'':
		end := strings.IndexByte(p.src[p.pos+1:], '\'')
		if end < 0 {
			return nil, fmt.Errorf("unterminated string")
		}
		s := p.src[p.pos+1 : p.pos+1+end]
		p.pos += end + 2
		return s, nil
	case c == '(':
		p.pos++
		v, err := p.or()
		if err != nil || !p.eat(")") {
			return nil, fmt.Errorf("unbalanced parenthesis")
		}
		return v, nil
	case c == '[':
		p.pos++
		var list []any
		for !p.eat("]") {
			v, err := p.primary()
			if err != nil {
				return nil, err
			}
			list = append(list, v)
			p.eat(",")
		}
		return list, nil
	case c >= '0' && c <= '9':
		start := p.pos
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
		n, _ := strconv.Atoi(p.src[start:p.pos])
		return n, nil
	}
	name := p.ident()
	if name == "" {
		return nil, fmt.Errorf("unexpected %q", p.src[p.pos:])
	}
	if p.eat("(") {
		args, err := p.args()
		if err != nil || len(args) != 1 {
			return nil, fmt.Errorf("function %s takes one argument", name)
		}
		switch name {
		case "string":
			return fmt.Sprint(args[0]), nil
		case "int":
			n, _ := strconv.Atoi(strings.TrimSuffix(fmt.Sprint(args[0]), ".0"))
			return n, nil
		}
		return nil, fmt.Errorf("function %q is unsupported", name)
	}
	if name != "body" {
		return nil, fmt.Errorf("identifier %q is unsupported", name)
	}
	var cur any = p.body
	for p.pos < len(p.src) && p.src[p.pos] == '.' {
		save := p.pos
		p.pos++
		field := p.ident()
		p.skip()
		if p.pos < len(p.src) && p.src[p.pos] == '(' {
			p.pos = save
			break
		}
		m, ok := cur.(map[string]any)
		if !ok {
			cur = nil
			continue
		}
		cur = m[field]
	}
	return cur, nil
}

// bodyPath reads a dotted path such as body.pull_request.head.ref from the payload.
func bodyPath(body map[string]any, path string) any {
	var cur any = body
	for i, part := range strings.Split(path, ".") {
		if i == 0 && part == "body" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}
