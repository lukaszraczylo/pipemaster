package expr_test

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
)

const (
	dirFwd = "gha-gitlab"
	dirRev = "gitlab-gha"
)

var vectorRefs = map[string][]string{
	"ref_name": {"ref_name"}, "ref_type": {"ref_type"}, "event": {"event"}, "sha": {"sha"},
	"branch": {"branch"}, "tag": {"tag"},
	"gha_native": {"gha", "github", "run_id"}, "gitlab_native": {"gitlab", "CI_JOB_NAME"},
}

// decodeExpr reads the array encoding of E from lean4/GenVectors.lean; the source dialect names any opaque node.
func decodeExpr(raw json.RawMessage, dialect string) (expr.Expr, error) {
	var a []json.RawMessage
	if err := json.Unmarshal(raw, &a); err != nil || len(a) == 0 {
		return nil, fmt.Errorf("bad node %s", raw)
	}
	var tag string
	if err := json.Unmarshal(a[0], &tag); err != nil {
		return nil, err
	}
	sub := func(i int) (expr.Expr, error) { return decodeExpr(a[i], dialect) }
	str := func(i int) string { var s string; _ = json.Unmarshal(a[i], &s); return s }
	switch tag {
	case "null":
		return expr.Lit{}, nil
	case "bool":
		var b bool
		err := json.Unmarshal(a[1], &b)
		return expr.Lit{Kind: expr.LitBool, Bool: b}, err
	case "str":
		return expr.Lit{Kind: expr.LitStr, Str: str(1)}, nil
	case "num":
		var f float64
		err := json.Unmarshal(a[1], &f)
		return expr.Lit{Kind: expr.LitNum, Num: f}, err
	case "ref":
		p, ok := vectorRefs[str(1)]
		if !ok {
			return nil, fmt.Errorf("unknown ref %q", str(1))
		}
		return expr.Ref{Path: p}, nil
	case "opq":
		return expr.Opaque{Dialect: dialect, Text: "x"}, nil
	case "not":
		x, err := sub(1)
		return expr.Unary{Op: expr.OpNot, X: x}, err
	case "call":
		x, err := sub(2)
		return expr.Call{Name: str(1), Args: []expr.Expr{x}}, err
	case "startsWith", "endsWith", "contains":
		l, err := sub(1)
		if err != nil {
			return nil, err
		}
		r, err := sub(2)
		return expr.Call{Name: tag, Args: []expr.Expr{l, r}}, err
	case "rx", "nrx":
		x, err := sub(1)
		if err != nil {
			return nil, err
		}
		op := expr.OpMatch
		if tag == "nrx" {
			op = expr.OpNoMatch
		}
		m := map[string]expr.Mode{"exact": expr.ModeExact, "prefix": expr.ModePrefix, "suffix": expr.ModeSuffix, "sub": expr.ModeSub}[str(2)]
		return expr.Binary{Op: op, L: x, R: expr.Lit{Kind: expr.LitStr, Str: expr.Pattern(m, str(3))}}, nil
	case "rawRx":
		x, err := sub(1)
		return expr.Binary{Op: expr.OpMatch, L: x, R: expr.Lit{Kind: expr.LitStr, Str: str(2)}}, err
	case "and", "or", "eq", "ne", "lt":
		l, err := sub(1)
		if err != nil {
			return nil, err
		}
		r, err := sub(2)
		op := map[string]string{"and": expr.OpAnd, "or": expr.OpOr, "eq": expr.OpEq, "ne": expr.OpNe, "lt": expr.OpLt}[tag]
		return expr.Binary{Op: op, L: l, R: r}, err
	}
	return nil, fmt.Errorf("unknown tag %q", tag)
}

// fataler is the part of testing.T that rapid.T shares.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

func holdsOf(t fataler, e expr.Expr, v view, sem expr.Semantics) bool {
	t.Helper()
	val, err := expr.Eval(e, v, sem)
	if err != nil {
		t.Fatalf("Eval(%s): %v", expr.String(e), err)
	}
	if sem.Truthy != nil {
		return sem.Truthy(val)
	}
	switch x := val.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

type vectorOut struct {
	Tr  json.RawMessage `json:"tr"`
	Src []bool          `json:"src"`
	Tgt []bool          `json:"tgt"`
}

// TestTranslateVectors checks Go against the Lean model on every generated case: the same translation
// (or the same refusal), Translatable agreeing with Translate, and Go's evaluation of source and target
// giving the truthiness values Lean computed.
func TestTranslateVectors(t *testing.T) {
	rawEnvs, cases := vectors.LoadWithEnvs(t, "expr")
	var envMaps []map[string]*string
	if err := json.Unmarshal(rawEnvs, &envMaps); err != nil || len(envMaps) < 4 {
		t.Fatalf("envs: %v (%d)", err, len(envMaps))
	}
	cenvs := make([]canonEnv, len(envMaps))
	for i, m := range envMaps {
		cenvs[i] = canonEnv{RefName: m["ref_name"], RefType: m["ref_type"], Event: m["event"], SHA: m["sha"], Nat: m["native"]}
	}
	count := map[string]int{}
	for i, c := range cases {
		var in struct {
			Dir  string          `json:"dir"`
			Expr json.RawMessage `json:"expr"`
		}
		var out vectorOut
		if err := json.Unmarshal(c.In, &in); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		from, to := gha.Lang, gitlab.Lang
		srcView := func(e canonEnv) view { return e.ghaView() }
		tgtView := func(e canonEnv) view { return e.gitlabView() }
		if in.Dir == dirRev {
			from, to = gitlab.Lang, gha.Lang
			srcView, tgtView = tgtView, srcView
		}
		src, err := decodeExpr(in.Expr, from.ID)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		got, ok := expr.Translate(src, from, to)
		if tb := expr.Translatable(src, from, to); tb != ok {
			t.Errorf("case %d %s: Translatable = %v but Translate ok = %v", i, expr.String(src), tb, ok)
		}
		if string(out.Tr) == "null" {
			count[in.Dir+" none"]++
			if ok {
				t.Errorf("case %d %s: Lean refuses, Go translated to %s", i, expr.String(src), expr.String(got))
			}
			continue
		}
		count[in.Dir+" some"]++
		want, err := decodeExpr(out.Tr, to.ID)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("case %d %s: Go = %v, %v; Lean = %s", i, expr.String(src), got, ok, expr.String(want))
			continue
		}
		checkEval(t, i, src, got, from, to, cenvs, srcView, tgtView, out)
	}
	for _, k := range []string{dirFwd + " some", dirFwd + " none", dirRev + " some", dirRev + " none"} {
		if count[k] < 100 {
			t.Errorf("only %d cases for %q; the vectors lost coverage", count[k], k)
		}
	}
	t.Logf("cases: %v", count)
}

func checkEval(t *testing.T, i int, src, got expr.Expr, from, to expr.Dialect, envs []canonEnv,
	srcView, tgtView func(canonEnv) view, out vectorOut) {
	t.Helper()
	if len(out.Src) != len(envs) || len(out.Tgt) != len(envs) {
		t.Fatalf("case %d: %d/%d results for %d envs", i, len(out.Src), len(out.Tgt), len(envs))
	}
	for j, e := range envs {
		s := holdsOf(t, src, srcView(e), from.Sem)
		g := holdsOf(t, got, tgtView(e), to.Sem)
		if s != out.Src[j] || g != out.Tgt[j] || s != g {
			t.Errorf("case %d env %d %s -> %s: Go src=%v tgt=%v, Lean src=%v tgt=%v",
				i, j, expr.String(src), expr.String(got), s, g, out.Src[j], out.Tgt[j])
		}
	}
	printable(t, i, got, to)
}

// printable holds the translation to what emitters need: the target dialect prints it and reads it back unchanged.
func printable(t *testing.T, i int, e expr.Expr, to expr.Dialect) {
	t.Helper()
	if !to.Supports(e) {
		t.Errorf("case %d: %s does not print in %s", i, expr.String(e), to.ID)
		return
	}
	var text string
	var err error
	var back expr.Expr
	if to.ID == "gitlab" {
		if text, err = gitlab.Print(e); err == nil {
			back, err = gitlab.Parse(text)
		}
	} else if text, err = gha.Print(e); err == nil {
		back, err = gha.Parse(text)
	}
	if err != nil || !reflect.DeepEqual(back, e) {
		t.Errorf("case %d: %s prints %q and reads back as %v (%v)", i, expr.String(e), text, back, err)
	}
}
