package tekton

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	statusAll    = "$(tasks.status)"
	statusFailed = "Failed"
	statusOK     = "Succeeded"
	statusNone   = "None"
	fnFailure    = "failure"
	fnSuccess    = "success"
	resultKey    = "result"
)

// taskStatus reads the variable $(tasks.<name>.status), which only a finally task can use.
var taskStatus = regexp.MustCompile(`^\$\(tasks\.([a-z0-9]([a-z0-9-]*[a-z0-9])?)\.status\)$`)

// taskResults maps a Tekton task status to the needs.<job>.result value.
var taskResults = map[string]string{statusFailed: "failure", statusOK: "success", statusNone: "skipped"}

// when reads the when entries of a task: all of them must hold.
func (b *jobBuilder) when(v any, line int) (expr.Expr, error) {
	entries, err := whenEntries(v)
	if err != nil {
		return nil, fmt.Errorf("line %d: %w", line, err)
	}
	return b.condition(entries, line, b.finally), nil
}

// whenEntries decodes a list of when entries.
func whenEntries(v any) ([]exprtekton.Entry, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("when must be a list")
	}
	var out []exprtekton.Entry
	for _, e := range list {
		m, ok := asMap(e)
		if !ok {
			return nil, fmt.Errorf("a when entry must be a mapping")
		}
		var en exprtekton.Entry
		en.Input, _ = text(m.Vals["input"])
		en.Operator, _ = text(m.Vals["operator"])
		en.CEL, _ = text(m.Vals["cel"])
		en.Values, _ = textList(m.Vals["values"])
		out = append(out, en)
	}
	return out, nil
}

// condition is the AND of the entries. A finally task can test the status of the run or of one task.
func (b *jobBuilder) condition(entries []exprtekton.Entry, line int, finally bool) expr.Expr {
	var out expr.Expr
	for _, en := range entries {
		e, ok := b.status(en, finally)
		if !ok {
			e = b.entry(en, line)
		}
		if out == nil {
			out = e
		} else {
			out = expr.Binary{Op: expr.OpAnd, L: out, R: e}
		}
	}
	return out
}

// resolveParams replaces an inputs.<param> ref in a step condition with what the PipelineTask binds to the param.
func (b *jobBuilder) resolveParams(e expr.Expr) expr.Expr {
	switch v := e.(type) {
	case expr.Ref:
		if len(v.Path) == 2 && v.Path[0] == expr.PrefixInputs {
			if parts, ok := b.tx.taskParam(v.Path[1]); ok && len(parts) == 1 && parts[0].Expr != nil {
				if r, isRef := parts[0].Expr.(expr.Ref); isRef {
					return r
				}
			}
		}
	case expr.Unary:
		return expr.Unary{Op: v.Op, X: b.resolveParams(v.X)}
	case expr.Binary:
		return expr.Binary{Op: v.Op, L: b.resolveParams(v.L), R: b.resolveParams(v.R)}
	}
	return e
}

// entry reads one entry through the shared decoder and renames the tasks it points at to job IDs.
func (b *jobBuilder) entry(en exprtekton.Entry, line int) expr.Expr {
	e := exprtekton.FromWhen([]exprtekton.Entry{en})
	if b.inTask {
		e = b.resolveParams(e)
	}
	if o, isOpaque := e.(expr.Opaque); isOpaque {
		what := "a when entry that is not one variable tested with in or notin"
		if en.CEL != "" {
			what = "a cel when entry"
		}
		raw, _ := json.Marshal(en)
		b.c.p.rep.Add(report.Entry{
			IRPath: b.path, Feature: caps.ExprOpaque, Level: caps.Lost, Strategy: "kept as opaque Tekton text",
			Detail: what + " is kept as opaque Tekton text: " + string(raw), SourceLoc: b.c.file + ":" + itoa(line),
		})
		return o
	}
	expr.NeedRefs(e, func(n string) {
		if _, ok := b.c.taskID(n); ok {
			b.whenNeeds[n] = true
		}
	})
	return expr.RenameNeeds(e, func(n string) string {
		if id, ok := b.c.taskID(n); ok {
			return id
		}
		return n
	})
}

// status maps a status entry of a finally task to the IR status functions and to needs.<job>.result.
func (b *jobBuilder) status(en exprtekton.Entry, finally bool) (expr.Expr, bool) {
	if !finally || len(en.Values) != 1 || en.CEL != "" || en.Operator != exprtekton.OpIn && en.Operator != exprtekton.OpNotIn {
		return nil, false
	}
	val, neg := en.Values[0], en.Operator == exprtekton.OpNotIn
	if en.Input == statusAll {
		fn := map[string]string{statusFailed: fnFailure, statusOK: fnSuccess}[val]
		if fn == "" {
			return nil, false
		}
		var e expr.Expr = expr.Call{Name: fn}
		if neg {
			e = expr.Unary{Op: expr.OpNot, X: e}
		}
		return e, true
	}
	m := taskStatus.FindStringSubmatch(en.Input)
	res, known := taskResults[val]
	if m == nil || !known {
		return nil, false
	}
	id, ok := b.c.taskID(m[1])
	if !ok {
		return nil, false
	}
	b.whenNeeds[m[1]] = true
	op := expr.OpEq
	if neg {
		op = expr.OpNe
	}
	return expr.Binary{
		Op: op, L: expr.Ref{Path: []string{expr.PrefixNeeds, id, resultKey}}, R: expr.Lit{Kind: expr.LitStr, Str: res},
	}, true
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
