package emitkit

import (
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

var plainName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// RewriteNeedsRefs renames the job ids that `needs.<id>` references name, in place, across every expression
// reference and every ${{ }} part of a string under v. All renames apply at once, so swapped ids stay correct. v must be a pointer.
func RewriteNeedsRefs(v any, renames map[string]string) { rewriteRefs(v, expr.PrefixNeeds, renames) }

// RewriteInputRefs renames the inputs that `inputs.<name>` references name, with the rules of RewriteNeedsRefs.
func RewriteInputRefs(v any, renames map[string]string) { rewriteRefs(v, expr.PrefixInputs, renames) }

func rewriteRefs(v any, prefix string, renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	rw := &refRewriter{renames: renames, prefix: prefix}
	olds := make([]string, 0, len(renames))
	for o := range renames {
		olds = append(olds, regexp.QuoteMeta(o))
	}
	sort.Slice(olds, func(i, j int) bool { return len(olds[i]) > len(olds[j]) })
	rw.re = regexp.MustCompile(`\b` + prefix + `(\.|\[['"])(` + strings.Join(olds, "|") + `)(['"]\]|[^A-Za-z0-9_'"-]|$)`)
	rw.walk(reflect.ValueOf(v))
}

type refRewriter struct {
	re      *regexp.Regexp
	renames map[string]string
	prefix  string
}

var templateExpr = regexp.MustCompile(`\$\{\{.*?\}\}`)

// text rewrites references inside ${{ }} only; the text around them is shell or prose and stays as written.
func (r *refRewriter) text(s string) string {
	if !strings.Contains(s, "${{") {
		return s
	}
	return templateExpr.ReplaceAllStringFunc(s, r.expression)
}

func (r *refRewriter) expression(s string) string {
	return r.re.ReplaceAllStringFunc(s, func(m string) string {
		sub := r.re.FindStringSubmatch(m)
		id, open, tail := r.renames[sub[2]], sub[1], sub[3]
		if open == "." && !plainName.MatchString(id) {
			// An id that is not a name can only be written in bracket form.
			open, tail = "['", "']"+tail
		}
		return r.prefix + open + id + tail
	})
}

func (r *refRewriter) walk(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			r.walk(v.Elem())
		}
	case reflect.Interface:
		if v.IsNil() || !v.CanSet() {
			return
		}
		cp := reflect.New(v.Elem().Type()).Elem()
		cp.Set(v.Elem())
		r.walk(cp)
		v.Set(cp)
	case reflect.Struct:
		if v.CanAddr() {
			if ref, ok := v.Addr().Interface().(*expr.Ref); ok {
				r.ref(ref)
				return
			}
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				r.walk(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			r.walk(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			cp := reflect.New(v.Type().Elem()).Elem()
			cp.Set(v.MapIndex(k))
			r.walk(cp)
			v.SetMapIndex(k, cp)
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(r.text(v.String()))
		}
	}
}

func (r *refRewriter) ref(ref *expr.Ref) {
	if len(ref.Path) > 1 && ref.Path[0] == r.prefix {
		if n, ok := r.renames[ref.Path[1]]; ok {
			p := append([]string(nil), ref.Path...)
			p[1] = n
			ref.Path = p
		}
	}
}
