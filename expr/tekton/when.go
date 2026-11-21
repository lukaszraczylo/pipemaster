package tekton

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Tekton `when` operators.
const (
	OpIn    = "in"
	OpNotIn = "notin"
)

// Disabled is the input and value of the `when` that is always false.
const (
	disabledInput = "pipemaster-disabled"
	disabledValue = "never"
)

// Entry is one `when` entry. All entries of a task must hold. CEL replaces the other fields and is alpha.
type Entry struct {
	Input    string   `json:"input,omitempty" yaml:"input,omitempty"`
	Operator string   `json:"operator,omitempty" yaml:"operator,omitempty"`
	CEL      string   `json:"cel,omitempty" yaml:"cel,omitempty"`
	Values   []string `json:"values,omitempty" yaml:"values,omitempty"`
}

// DisabledWhen is the fail-safe for a condition that has no `when` form: it skips the task.
func DisabledWhen() []Entry {
	return []Entry{{Input: disabledInput, Operator: OpIn, Values: []string{disabledValue}}}
}

// IsDisabled reports whether es is exactly DisabledWhen.
func IsDisabled(es []Entry) bool {
	return slices.EqualFunc(es, DisabledWhen(), func(a, b Entry) bool {
		return a.Input == b.Input && a.Operator == b.Operator && a.CEL == b.CEL && slices.Equal(a.Values, b.Values)
	})
}

// FromWhen reads a task's `when` entries as the AND of one expression per entry. An entry that is not one
// variable tested with in or notin, such as a cel entry or a literal input, is an Opaque node. No entries
// read as nil.
func FromWhen(entries []Entry) expr.Expr {
	var out expr.Expr
	for _, en := range entries {
		e := decode(en)
		if out == nil {
			out = e
		} else {
			out = expr.Binary{Op: expr.OpAnd, L: out, R: e}
		}
	}
	return out
}

func decode(en Entry) expr.Expr {
	op := expr.OpEq
	join := expr.OpOr
	if en.Operator == OpNotIn {
		op, join = expr.OpNe, expr.OpAnd
	}
	r, ok := FromWord(en.Input)
	if en.CEL != "" || !ok || len(en.Values) == 0 || en.Operator != OpIn && en.Operator != OpNotIn {
		return opaque(en)
	}
	var out expr.Expr
	for _, v := range en.Values {
		cmp := expr.Binary{Op: op, L: r, R: expr.Lit{Kind: expr.LitStr, Str: v}}
		if out == nil {
			out = cmp
		} else {
			out = expr.Binary{Op: join, L: out, R: cmp}
		}
	}
	return out
}

// opaque keeps the entry as JSON text, which OpaqueEntry reads back.
func opaque(en Entry) expr.Opaque {
	b, err := json.Marshal(en)
	if err != nil {
		return expr.Opaque{Dialect: Dialect, Text: fmt.Sprintf("%+v", en)}
	}
	return expr.Opaque{Dialect: Dialect, Text: string(b)}
}

// OpaqueEntry reads the entry back from an Opaque node that FromWhen made.
func OpaqueEntry(o expr.Opaque) (Entry, bool) {
	var en Entry
	if o.Dialect != Dialect || json.Unmarshal([]byte(o.Text), &en) != nil {
		return Entry{}, false
	}
	return en, true
}

// ToWhen writes e as `when` entries. It accepts an AND of conditions that each test one ref against string
// literals: an OR of equalities is in, and an AND of inequalities is notin. Anything else fails, including
// Opaque, because a cel entry is alpha and never emitted. Adjacent notin entries on one ref merge.
func ToWhen(e expr.Expr) ([]Entry, bool) {
	if e == nil {
		return nil, false
	}
	var out []Entry
	for _, c := range flatten(e, expr.OpAnd) {
		en, ok := entryOf(c)
		if !ok {
			return nil, false
		}
		if last := len(out) - 1; last >= 0 && en.Operator == OpNotIn && out[last].Operator == OpNotIn && out[last].Input == en.Input {
			out[last].Values = append(out[last].Values, en.Values...)
			continue
		}
		out = append(out, en)
	}
	return out, true
}

// flatten lists the operands of a chain of op, left to right.
func flatten(e expr.Expr, op string) []expr.Expr {
	if b, ok := e.(expr.Binary); ok && b.Op == op {
		return append(flatten(b.L, op), flatten(b.R, op)...)
	}
	return []expr.Expr{e}
}

func entryOf(e expr.Expr) (Entry, bool) {
	if b, ok := e.(expr.Binary); ok && b.Op == expr.OpNe {
		in, v, ok := compare(b)
		return Entry{Input: in, Operator: OpNotIn, Values: []string{v}}, ok
	}
	var input string
	var values []string
	for i, c := range flatten(e, expr.OpOr) {
		b, ok := c.(expr.Binary)
		if !ok || b.Op != expr.OpEq {
			return Entry{}, false
		}
		in, v, ok := compare(b)
		if !ok || i > 0 && in != input {
			return Entry{}, false
		}
		input = in
		values = append(values, v)
	}
	return Entry{Input: input, Operator: OpIn, Values: values}, true
}

// compare reads a ref against a string literal, in either order.
func compare(b expr.Binary) (input, value string, ok bool) {
	r, lit := b.L, b.R
	if _, isRef := r.(expr.Ref); !isRef {
		r, lit = b.R, b.L
	}
	ref, isRef := r.(expr.Ref)
	l, isLit := lit.(expr.Lit)
	if !isRef || !isLit || l.Kind != expr.LitStr {
		return "", "", false
	}
	word, ok := RefWord(ref)
	return word, l.Str, ok
}
