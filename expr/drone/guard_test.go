package drone_test

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/drone"
)

func TestPrintGuard(t *testing.T) {
	for _, c := range []struct {
		e          expr.Expr
		name, want string
	}{
		{eq(branch, "main"), "equality", `if ! [ "$DRONE_BRANCH" = 'main' ]; then exit 0; fi # pipemaster:guard`},
		{bin(expr.OpNe, event, str("tag")), "inequality", `if ! [ "$DRONE_BUILD_EVENT" != 'tag' ]; then exit 0; fi # pipemaster:guard`},
		{eq(r("env", "DEPLOY"), "yes"), "user env", `if ! [ "$DEPLOY" = 'yes' ]; then exit 0; fi # pipemaster:guard`},
		{bin(expr.OpEq, branch, r("repo")), "ref against ref", `if ! [ "$DRONE_BRANCH" = "$DRONE_REPO" ]; then exit 0; fi # pipemaster:guard`},
		{eq(r("tag"), ""), "empty literal", `if ! [ "$DRONE_TAG" = '' ]; then exit 0; fi # pipemaster:guard`},
		{eq(branch, "it's"), "single quote", `if ! [ "$DRONE_BRANCH" = 'it'\''s' ]; then exit 0; fi # pipemaster:guard`},
		{eq(branch, "$HOME `id` ${x}"), "dollar and backtick", "if ! [ \"$DRONE_BRANCH\" = '$HOME `id` ${x}' ]; then exit 0; fi # pipemaster:guard"},
		{eq(branch, `a "b" c`), "spaces and double quote", `if ! [ "$DRONE_BRANCH" = 'a "b" c' ]; then exit 0; fi # pipemaster:guard`},
		{eq(branch, "-n"), "test operators as data", `if ! [ "$DRONE_BRANCH" = '-n' ]; then exit 0; fi # pipemaster:guard`},
		{bin(expr.OpAnd, bin(expr.OpAnd, eq(branch, "a"), eq(r("repo"), "b")), eq(r("tag"), "c")), "and chains bare", `if ! ( [ "$DRONE_BRANCH" = 'a' ] && [ "$DRONE_REPO" = 'b' ] && [ "$DRONE_TAG" = 'c' ] ); then exit 0; fi # pipemaster:guard`},
		{bin(expr.OpOr, bin(expr.OpAnd, eq(event, "pull_request"), eq(branch, "main")), eq(event, "schedule")), "mixed operators group", `if ! ( ( [ "$DRONE_BUILD_EVENT" = 'pull_request' ] && [ "$DRONE_BRANCH" = 'main' ] ) || [ "$DRONE_BUILD_EVENT" = 'cron' ] ); then exit 0; fi # pipemaster:guard`},
		{bin(expr.OpAnd, eq(branch, "a"), bin(expr.OpAnd, eq(r("repo"), "b"), eq(r("tag"), "c"))), "right nesting groups", `if ! ( [ "$DRONE_BRANCH" = 'a' ] && ( [ "$DRONE_REPO" = 'b' ] && [ "$DRONE_TAG" = 'c' ] ) ); then exit 0; fi # pipemaster:guard`},
		{not(eq(branch, "a")), "not", `if ! ( ! [ "$DRONE_BRANCH" = 'a' ] ); then exit 0; fi # pipemaster:guard`},
		{not(bin(expr.OpOr, eq(branch, "a"), eq(branch, "b"))), "not of group", `if ! ( ! ( [ "$DRONE_BRANCH" = 'a' ] || [ "$DRONE_BRANCH" = 'b' ] ) ); then exit 0; fi # pipemaster:guard`},
		{not(not(eq(branch, "a"))), "double not", `if ! ( ! ( ! [ "$DRONE_BRANCH" = 'a' ] ) ); then exit 0; fi # pipemaster:guard`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := drone.PrintGuard(c.e, exact)
			if err != nil || got != c.want {
				t.Fatalf("PrintGuard =\n%s\nerr %v; want\n%s", got, err, c.want)
			}
			back, ok := drone.ParseGuard(got)
			if !ok || !reflect.DeepEqual(back, c.e) {
				t.Errorf("ParseGuard = %#v, %v; want %#v", back, ok, c.e)
			}
		})
	}
}

func TestNotGuardable(t *testing.T) {
	for name, e := range map[string]expr.Expr{
		"call":            expr.Call{Name: "success"},
		"opaque glob":     drone.GlobOpaque("branch", "release/*"),
		"bare ref":        branch,
		"bare literal":    str("x"),
		"ordering":        bin(expr.OpLt, branch, str("x")),
		"regex":           bin(expr.OpMatch, branch, str("^x")),
		"number literal":  bin(expr.OpEq, branch, expr.Lit{Kind: expr.LitNum, Num: 1}),
		"null literal":    bin(expr.OpEq, branch, expr.Lit{}),
		"bool literal":    expr.Lit{Kind: expr.LitBool, Bool: true},
		"head ref":        eq(r("pr", "head_ref"), "x"),
		"env of builtin":  eq(r("env", "DRONE_BRANCH"), "x"),
		"env odd name":    eq(r("env", "a-b"), "x"),
		"newline literal": eq(branch, "a\nb"),
		"return literal":  eq(branch, "a\rb"),
		"nul literal":     eq(branch, "a\x00b"),
		"nested bad":      bin(expr.OpAnd, eq(branch, "a"), expr.Call{Name: "failure"}),
		"unary non-not":   expr.Unary{Op: "-", X: eq(branch, "a")},
		"nil":             nil,
		"inside not":      not(expr.Call{Name: "success"}),
		"right of or":     bin(expr.OpOr, eq(branch, "a"), drone.GlobOpaque("branch", "*")),
		"foreign opaque":  expr.Opaque{Dialect: "gha", Text: "x"},
	} {
		if drone.Guardable(e, exact) {
			t.Errorf("%s: Guardable = true", name)
		}
		if line, err := drone.PrintGuard(e, exact); !errors.Is(err, drone.ErrNotGuardable) {
			t.Errorf("%s: PrintGuard = %q, %v; want ErrNotGuardable", name, line, err)
		}
	}
}

func TestParseGuardRejectsOtherText(t *testing.T) {
	good, err := drone.PrintGuard(eq(branch, "main"), exact)
	if err != nil {
		t.Fatal(err)
	}
	for name, line := range map[string]string{
		"no marker":        strings.TrimSuffix(good, " "+drone.GuardMarker),
		"author line":      `if ! [ "$DRONE_BRANCH" = 'main' ]; then exit 0; fi`,
		"other script":     "echo hi",
		"empty":            "",
		"extra spaces":     strings.Replace(good, "[ ", "[  ", 1),
		"unquoted var":     strings.Replace(good, `"$DRONE_BRANCH"`, "$DRONE_BRANCH", 1),
		"adjacent pieces":  strings.Replace(good, `'main'`, `'ma''in'`, 1),
		"trailing command": good + "; rm -rf /",
		"mixed chain":      `if ! ( [ "$A" = 'a' ] && [ "$B" = 'b' ] || [ "$C" = 'c' ] ); then exit 0; fi ` + drone.GuardMarker,
		"double bang":      `if ! ( ! ! [ "$A" = 'a' ] ); then exit 0; fi ` + drone.GuardMarker,
		"unterminated":     `if ! [ "$A" = 'a ]; then exit 0; fi ` + drone.GuardMarker,
		"bad var name":     `if ! [ "$A B" = 'a' ]; then exit 0; fi ` + drone.GuardMarker,
		"command in group": `if ! ( rm x ); then exit 0; fi ` + drone.GuardMarker,
	} {
		if e, ok := drone.ParseGuard(line); ok {
			t.Errorf("%s: ParseGuard accepted %q as %#v", name, line, e)
		}
	}
}

func TestAddRemoveGuard(t *testing.T) {
	e := bin(expr.OpAnd, eq(event, "pull_request"), eq(branch, "it's"))
	script := []string{"make test", "make release"}
	guarded, err := drone.AddGuard(script, e, exact)
	if err != nil || len(guarded) != 3 || !reflect.DeepEqual(guarded[1:], script) {
		t.Fatalf("AddGuard = %q, %v", guarded, err)
	}
	if len(script) != 2 {
		t.Error("AddGuard changed its input")
	}
	rest, got, ok := drone.RemoveGuard(guarded)
	if !ok || !reflect.DeepEqual(got, e) || !reflect.DeepEqual(rest, script) {
		t.Errorf("RemoveGuard = %q, %#v, %v", rest, got, ok)
	}
	if rest, _, ok := drone.RemoveGuard(script); ok || !reflect.DeepEqual(rest, script) {
		t.Errorf("RemoveGuard on a plain script = %q, %v", rest, ok)
	}
	if _, _, ok := drone.RemoveGuard(nil); ok {
		t.Error("RemoveGuard on no script")
	}
	if _, err := drone.AddGuard(script, expr.Call{Name: "success"}, exact); !errors.Is(err, drone.ErrNotGuardable) {
		t.Errorf("AddGuard on a call = %v", err)
	}
	// A guard anywhere but first is the author's line.
	if _, _, ok := drone.RemoveGuard([]string{"echo", guarded[0]}); ok {
		t.Error("a guard line after the first line was removed")
	}
}

var hostile = []string{"main", "", "it's", `a "b"`, "$HOME", "`id`", "a b", `\`, "-n", "!", "(", "*", "x;rm", "a'b'c", "é", "'"}

var guardRefs = []expr.Ref{
	r("branch"), r("tag"), r("sha"), r("repo"), r("actor"), r("pr", "number"), r("ref"), r("env", "FOO"), r("env", "_b1"),
}

func genOperand(t *rapid.T, label string) expr.Expr {
	if rapid.Bool().Draw(t, label+"isRef") {
		return rapid.SampledFrom(guardRefs).Draw(t, label+"ref")
	}
	return str(rapid.SampledFrom(hostile).Draw(t, label+"lit"))
}

func genGuardable(t *rapid.T, depth int) expr.Expr {
	k := rapid.IntRange(0, 3).Draw(t, "k")
	if depth <= 0 || k == 0 {
		return bin(rapid.SampledFrom([]string{expr.OpEq, expr.OpNe}).Draw(t, "cmp"), genOperand(t, "l"), genOperand(t, "r"))
	}
	if k == 1 {
		return not(genGuardable(t, depth-1))
	}
	return bin(rapid.SampledFrom([]string{expr.OpAnd, expr.OpOr}).Draw(t, "op"), genGuardable(t, depth-1), genGuardable(t, depth-1))
}

// Printed guard text must read back to the same AST, whatever literal it holds.
func TestGuardRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		e := genGuardable(t, 4)
		line, err := drone.PrintGuard(e, exact)
		if err != nil {
			t.Fatalf("PrintGuard(%#v): %v", e, err)
		}
		back, ok := drone.ParseGuard(line)
		if !ok || !reflect.DeepEqual(back, e) {
			t.Fatalf("ParseGuard(%s) = %#v, %v; want %#v", line, back, ok, e)
		}
		if again, err := drone.PrintGuard(back, exact); err != nil || again != line {
			t.Fatalf("reprint = %q, %v; want %q", again, err, line)
		}
	})
}

func evalGuard(e expr.Expr, env map[string]string) bool {
	val := func(x expr.Expr) string {
		if l, ok := x.(expr.Lit); ok {
			return l.Str
		}
		name, _ := drone.VarName(x.(expr.Ref))
		return env[name]
	}
	switch v := e.(type) {
	case expr.Unary:
		return !evalGuard(v.X, env)
	case expr.Binary:
		switch v.Op {
		case expr.OpAnd:
			return evalGuard(v.L, env) && evalGuard(v.R, env)
		case expr.OpOr:
			return evalGuard(v.L, env) || evalGuard(v.R, env)
		case expr.OpEq:
			return val(v.L) == val(v.R)
		}
		return val(v.L) != val(v.R)
	}
	panic("unreachable")
}

// A real POSIX shell must reach the same verdict as the AST: the step runs exactly when the condition holds.
func TestGuardAgreesWithShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	rapid.Check(t, func(t *rapid.T) {
		e := genGuardable(t, 3)
		line, err := drone.PrintGuard(e, exact)
		if err != nil {
			t.Fatal(err)
		}
		env := map[string]string{}
		var environ []string
		for _, v := range []string{"DRONE_BRANCH", "DRONE_TAG", "DRONE_BUILD_EVENT", "DRONE_COMMIT_SHA", "DRONE_REPO",
			"DRONE_COMMIT_AUTHOR", "DRONE_PULL_REQUEST", "DRONE_COMMIT_REF", "FOO", "_b1"} {
			env[v] = rapid.SampledFrom(hostile).Draw(t, v)
			environ = append(environ, v+"="+env[v])
		}
		cmd := exec.Command(sh, "-c", line+"\necho ran") //nolint:gosec // sh is a looked-up path and the script is generated by the test
		cmd.Env = environ
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sh failed on %s: %v\n%s", line, err, out)
		}
		if ran, want := string(out) == "ran\n", evalGuard(e, env); ran != want {
			t.Fatalf("%s with %v: shell ran=%v, AST says %v (output %q)", line, environ, ran, want, out)
		}
	})
}

func TestGuardEventsTranslate(t *testing.T) {
	got, err := drone.PrintGuard(bin(expr.OpNe, event, str("schedule")), exact)
	want := `if ! [ "$DRONE_BUILD_EVENT" != 'cron' ]; then exit 0; fi # pipemaster:guard`
	if err != nil || got != want {
		t.Fatalf("PrintGuard = %q, %v; want %q", got, err, want)
	}
	back, ok := drone.ParseGuard(got)
	if !ok || !reflect.DeepEqual(back, bin(expr.OpNe, event, str("schedule"))) {
		t.Errorf("ParseGuard = %#v, %v; want the canonical event", back, ok)
	}
	// A bare Drone push is push without tag; the canonical AST has no node for it.
	if e, ok := drone.ParseGuard(`if ! [ "$DRONE_BUILD_EVENT" = 'push' ]; then exit 0; fi # pipemaster:guard`); ok {
		t.Errorf("a Drone push guard parsed as %#v", e)
	}
}

// Under the shell, the guard must run the step exactly when the canonical event test holds for the Drone value.
func TestGuardEventsAgreeWithShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	canon := map[string]string{"cron": "schedule", "custom": "workflow_dispatch", "pull_request": "pull_request",
		"tag": "tag", "promote": "promote", "rollback": "rollback"}
	for lit := range canon {
		for _, op := range []string{expr.OpEq, expr.OpNe} {
			e := bin(op, event, str(canon[lit]))
			line, err := drone.PrintGuard(e, exact)
			if err != nil {
				t.Fatal(err)
			}
			for env := range canon {
				cmd := exec.Command(sh, "-c", line+"\necho ran") //nolint:gosec // generated test script
				cmd.Env = []string{"DRONE_BUILD_EVENT=" + env}
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s: %v %s", line, err, out)
				}
				holds := env == lit
				if op == expr.OpNe {
					holds = !holds
				}
				if ran := string(out) == "ran\n"; ran != holds {
					t.Errorf("%s with event %s: ran=%v want %v", line, env, ran, holds)
				}
			}
		}
	}
}

// A folding source compares case-insensitively and the shell test does not, so only an uncased literal agrees.
func TestFoldingSourceGuards(t *testing.T) {
	for name, e := range map[string]expr.Expr{
		"cased literal":   eq(branch, "Main"),
		"lower literal":   eq(branch, "main"),
		"inequality":      bin(expr.OpNe, branch, str("main")),
		"ref against ref": bin(expr.OpEq, branch, r("repo")),
		"two literals":    bin(expr.OpEq, str("a"), str("b")),
		"inside not":      not(eq(branch, "main")),
		"non-ASCII":       eq(branch, "é"),
	} {
		if drone.Guardable(e, folding) {
			t.Errorf("%s: guardable under a folding source", name)
		}
		if !drone.Guardable(e, exact) {
			t.Errorf("%s: must stay guardable for an exact source", name)
		}
	}
	for name, e := range map[string]expr.Expr{
		"digits":        eq(r("pr", "number"), "42"),
		"punctuation":   bin(expr.OpNe, branch, str("-")),
		"empty":         eq(r("tag"), ""),
		"event literal": eq(event, "Schedule"),
	} {
		if !drone.Guardable(e, folding) {
			t.Errorf("%s: an uncased comparison is exact and must stay guardable", name)
		}
	}
	line, err := drone.PrintGuard(eq(event, "Schedule"), folding)
	if err != nil || !strings.Contains(line, `'cron'`) {
		t.Errorf("folded event guard = %q, %v; want the lowercase Drone value", line, err)
	}
}

func TestGuardPush(t *testing.T) {
	const v = `"$DRONE_BUILD_EVENT"`
	for _, c := range []struct {
		e    expr.Expr
		name string
		want string
	}{
		{eq(event, "push"), "equals", `if ! ( [ ` + v + ` = 'push' ] || [ ` + v + ` = 'tag' ] ); then exit 0; fi # pipemaster:guard`},
		{bin(expr.OpNe, event, str("push")), "not equals", `if ! ( [ ` + v + ` != 'push' ] && [ ` + v + ` != 'tag' ] ); then exit 0; fi # pipemaster:guard`},
		{not(eq(event, "push")), "negation", `if ! ( ! ( [ ` + v + ` = 'push' ] || [ ` + v + ` = 'tag' ] ) ); then exit 0; fi # pipemaster:guard`},
		{bin(expr.OpAnd, eq(event, "push"), eq(branch, "main")), "with branch",
			`if ! ( ( [ ` + v + ` = 'push' ] || [ ` + v + ` = 'tag' ] ) && [ "$DRONE_BRANCH" = 'main' ] ); then exit 0; fi # pipemaster:guard`},
		{bin(expr.OpOr, eq(event, "push"), eq(event, "tag")), "push or tag",
			`if ! ( ( [ ` + v + ` = 'push' ] || [ ` + v + ` = 'tag' ] ) || [ ` + v + ` = 'tag' ] ); then exit 0; fi # pipemaster:guard`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := drone.PrintGuard(c.e, exact)
			if err != nil || got != c.want {
				t.Fatalf("PrintGuard =\n%s\nerr %v; want\n%s", got, err, c.want)
			}
			back, ok := drone.ParseGuard(got)
			if !ok || !reflect.DeepEqual(back, c.e) {
				t.Errorf("ParseGuard = %#v, %v; want %#v", back, ok, c.e)
			}
		})
	}
	if g, err := drone.PrintGuard(eq(event, "Push"), folding); err != nil || !strings.Contains(g, `= 'push' ] || `) {
		t.Errorf("folded push guard = %q, %v", g, err)
	}
	if g, err := drone.PrintGuard(eq(event, "Push"), exact); err == nil {
		t.Errorf("an exact source printed a cased push: %q", g)
	}
	// Half a pair is the Drone push-without-tag set, which the canonical AST cannot name.
	for _, in := range []string{
		`if ! ( [ ` + v + ` = 'push' ] || [ ` + v + ` = 'cron' ] ); then exit 0; fi # pipemaster:guard`,
		`if ! ( [ ` + v + ` = 'tag' ] || [ ` + v + ` = 'push' ] ); then exit 0; fi # pipemaster:guard`,
		`if ! ( [ ` + v + ` != 'push' ] || [ ` + v + ` != 'tag' ] ); then exit 0; fi # pipemaster:guard`,
	} {
		if e, ok := drone.ParseGuard(in); ok {
			t.Errorf("ParseGuard(%q) = %#v; want a refusal", in, e)
		}
	}
}

var guardEvents = []string{"push", "schedule", "workflow_dispatch", "pull_request", "tag", "promote", "rollback"}

func genEventGuard(t *rapid.T, depth int) expr.Expr {
	k := rapid.IntRange(0, 3).Draw(t, "k")
	if depth <= 0 || k == 0 {
		op := rapid.SampledFrom([]string{expr.OpEq, expr.OpNe}).Draw(t, "cmp")
		return bin(op, event, str(rapid.SampledFrom(guardEvents).Draw(t, "ev")))
	}
	if k == 1 {
		return not(genEventGuard(t, depth-1))
	}
	return bin(rapid.SampledFrom([]string{expr.OpAnd, expr.OpOr}).Draw(t, "op"), genEventGuard(t, depth-1), genEventGuard(t, depth-1))
}

// droneSet is the DRONE_BUILD_EVENT values that a canonical event literal stands for.
func droneSet(canon string) []string {
	switch canon {
	case "push":
		return []string{"push", "tag"}
	case "schedule":
		return []string{"cron"}
	case "workflow_dispatch":
		return []string{"custom"}
	}
	return []string{canon}
}

func evalEvent(e expr.Expr, env string) bool {
	switch v := e.(type) {
	case expr.Unary:
		return !evalEvent(v.X, env)
	case expr.Binary:
		switch v.Op {
		case expr.OpAnd:
			return evalEvent(v.L, env) && evalEvent(v.R, env)
		case expr.OpOr:
			return evalEvent(v.L, env) || evalEvent(v.R, env)
		}
		in := false
		for _, d := range droneSet(v.R.(expr.Lit).Str) {
			in = in || d == env
		}
		return in == (v.Op == expr.OpEq)
	}
	panic("unreachable")
}

func TestEventGuardsRoundTripAndAgreeWithShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	rapid.Check(t, func(t *rapid.T) {
		e := genEventGuard(t, 3)
		line, err := drone.PrintGuard(e, exact)
		if err != nil {
			t.Fatal(err)
		}
		if back, ok := drone.ParseGuard(line); !ok || !reflect.DeepEqual(back, e) {
			t.Fatalf("ParseGuard(%s) = %#v, %v; want %#v", line, back, ok, e)
		}
		env := rapid.SampledFrom([]string{"push", "tag", "cron", "custom", "pull_request", "promote", "rollback"}).Draw(t, "env")
		cmd := exec.Command(sh, "-c", line+"\necho ran") //nolint:gosec // generated test script
		cmd.Env = []string{"DRONE_BUILD_EVENT=" + env}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", line, err, out)
		}
		if ran, want := string(out) == "ran\n", evalEvent(e, env); ran != want {
			t.Fatalf("%s with event %s: ran=%v, canonical says %v", line, env, ran, want)
		}
	})
}
