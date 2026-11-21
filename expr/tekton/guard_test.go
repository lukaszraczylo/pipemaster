package tekton_test

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/tekton"
)

func TestPrintGuard(t *testing.T) {
	for _, c := range []struct {
		e    expr.Expr
		name string
		want string
	}{
		{eq("branch", "main"), "context ref", `if ! [ "$PM_CTX_BRANCH" = 'main' ]; then exit 0; fi # pipemaster:guard`},
		{bin(expr.OpNe, ref("pr", "number"), str("1")), "dotted context name", `if ! [ "$PM_CTX_PR_NUMBER" != '1' ]; then exit 0; fi # pipemaster:guard`},
		{eq("ref_name", "x"), "underscore context name", `if ! [ "$PM_CTX_REF_NAME" = 'x' ]; then exit 0; fi # pipemaster:guard`},
		{bin(expr.OpEq, ref("inputs", "deploy-env"), str("prod")), "input", `if ! [ "$PM_INPUT_DEPLOY_ENV" = 'prod' ]; then exit 0; fi # pipemaster:guard`},
		{bin(expr.OpEq, ref("matrix", "os"), str("linux")), "matrix axis", `if ! [ "$PM_MATRIX_OS" = 'linux' ]; then exit 0; fi # pipemaster:guard`},
		{bin(expr.OpEq, ref("needs", "build-img", "outputs", "ok"), str("yes")), "task result", `if ! [ "$PM_NEEDS_BUILD_IMG__OK" = 'yes' ]; then exit 0; fi # pipemaster:guard`},
		{eq("branch", "it's"), "single quote", `if ! [ "$PM_CTX_BRANCH" = 'it'\''s' ]; then exit 0; fi # pipemaster:guard`},
		{eq("branch", "$(id) `x` ${y} $HOME"), "substitution text", "if ! [ \"$PM_CTX_BRANCH\" = '$(id) `x` ${y} $HOME' ]; then exit 0; fi # pipemaster:guard"},
		{eq("branch", `a "b" c`), "spaces and double quote", `if ! [ "$PM_CTX_BRANCH" = 'a "b" c' ]; then exit 0; fi # pipemaster:guard`},
		{eq("branch", "-n"), "test operator as data", `if ! [ "$PM_CTX_BRANCH" = '-n' ]; then exit 0; fi # pipemaster:guard`},
		{eq("branch", ""), "empty literal", `if ! [ "$PM_CTX_BRANCH" = '' ]; then exit 0; fi # pipemaster:guard`},
		{or(and(eq("branch", "a"), eq("tag", "b")), ne("repo", "c")), "mixed operators group",
			`if ! ( ( [ "$PM_CTX_BRANCH" = 'a' ] && [ "$PM_CTX_TAG" = 'b' ] ) || [ "$PM_CTX_REPO" != 'c' ] ); then exit 0; fi # pipemaster:guard`},
		{expr.Unary{Op: expr.OpNot, X: eq("branch", "a")}, "not", `if ! ( ! [ "$PM_CTX_BRANCH" = 'a' ] ); then exit 0; fi # pipemaster:guard`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := tekton.PrintGuard(c.e)
			if err != nil || got != c.want {
				t.Fatalf("PrintGuard =\n%s\nerr %v; want\n%s", got, err, c.want)
			}
			back, ok := tekton.ParseGuard(got)
			if !ok || !reflect.DeepEqual(back, c.e) {
				t.Errorf("ParseGuard = %#v, %v; want %#v", back, ok, c.e)
			}
		})
	}
}

func TestNotGuardable(t *testing.T) {
	for name, e := range map[string]expr.Expr{
		"call":             expr.Call{Name: "success"},
		"opaque":           expr.Opaque{Dialect: tekton.Dialect, Text: "x"},
		"bare ref":         ref("branch"),
		"ordering":         bin(expr.OpLt, ref("branch"), str("x")),
		"number literal":   bin(expr.OpEq, ref("branch"), expr.Lit{Kind: expr.LitNum, Num: 1}),
		"newline literal":  eq("branch", "a\nb"),
		"nul literal":      eq("branch", "a\x00b"),
		"env ref":          bin(expr.OpEq, ref("env", "X"), str("y")),
		"secret ref":       bin(expr.OpEq, ref("secrets", "X"), str("y")),
		"unknown context":  eq("workflow", "a"),
		"uppercase input":  bin(expr.OpEq, ref("inputs", "Env"), str("y")),
		"underscore input": bin(expr.OpEq, ref("inputs", "my_env"), str("y")),
		"double dash":      bin(expr.OpEq, ref("inputs", "a--b"), str("y")),
		"trailing dash":    bin(expr.OpEq, ref("inputs", "a-"), str("y")),
		"result with dash": bin(expr.OpEq, ref("needs", "a", "outputs", "b_c"), str("y")),
		"needs no output":  bin(expr.OpEq, ref("needs", "a"), str("y")),
	} {
		if tekton.Guardable(e) {
			t.Errorf("%s: Guardable = true", name)
		}
		if line, err := tekton.PrintGuard(e); !errors.Is(err, tekton.ErrNotGuardable) {
			t.Errorf("%s: PrintGuard = %q, %v; want ErrNotGuardable", name, line, err)
		}
	}
}

func TestParseGuardRejectsOtherText(t *testing.T) {
	good, err := tekton.PrintGuard(eq("branch", "main"))
	if err != nil {
		t.Fatal(err)
	}
	for name, line := range map[string]string{
		"empty":           "",
		"plain script":    "echo hi",
		"no marker":       `if ! [ "$PM_CTX_BRANCH" = 'main' ]; then exit 0; fi`,
		"trailing text":   good + " && rm -rf /",
		"unknown env":     `if ! [ "$HOME" = 'main' ]; then exit 0; fi # pipemaster:guard`,
		"unknown context": `if ! [ "$PM_CTX_NOPE" = 'main' ]; then exit 0; fi # pipemaster:guard`,
		"drone variable":  `if ! [ "$DRONE_BRANCH" = 'main' ]; then exit 0; fi # pipemaster:guard`,
		"mixed chain":     `if ! ( [ "$PM_CTX_BRANCH" = 'a' ] && [ "$PM_CTX_TAG" = 'b' ] || [ "$PM_CTX_REPO" = 'c' ] ); then exit 0; fi # pipemaster:guard`,
		"unterminated":    `if ! [ "$PM_CTX_BRANCH" = 'a ]; then exit 0; fi # pipemaster:guard`,
		"command in test": `if ! ( rm x ); then exit 0; fi # pipemaster:guard`,
		"lowercase input": `if ! [ "$PM_INPUT_a" = 'x' ]; then exit 0; fi # pipemaster:guard`,
	} {
		if e, ok := tekton.ParseGuard(line); ok {
			t.Errorf("%s: ParseGuard accepted %q as %#v", name, line, e)
		}
	}
}

func TestAddRemoveGuard(t *testing.T) {
	script := []string{"echo $(date)", "make"}
	e := eq("branch", "main")
	guarded, err := tekton.AddGuard(script, e)
	if err != nil || len(guarded) != 3 || guarded[1] != script[0] {
		t.Fatalf("AddGuard = %q, %v", guarded, err)
	}
	if len(script) != 2 {
		t.Error("AddGuard changed its input")
	}
	rest, got, ok := tekton.RemoveGuard(guarded)
	if !ok || !reflect.DeepEqual(rest, script) || !reflect.DeepEqual(got, e) {
		t.Errorf("RemoveGuard = %q, %#v, %v", rest, got, ok)
	}
	if rest, _, ok := tekton.RemoveGuard(script); ok || !reflect.DeepEqual(rest, script) {
		t.Errorf("RemoveGuard on a plain script = %q, %v", rest, ok)
	}
	if _, _, ok := tekton.RemoveGuard(nil); ok {
		t.Error("RemoveGuard on no script")
	}
	if _, err := tekton.AddGuard(script, expr.Call{Name: "success"}); !errors.Is(err, tekton.ErrNotGuardable) {
		t.Errorf("AddGuard on a call = %v", err)
	}
	if _, _, ok := tekton.RemoveGuard([]string{"echo", guarded[0]}); ok {
		t.Error("RemoveGuard found a guard that is not first")
	}
}

func TestGuardEnv(t *testing.T) {
	e := and(or(eq("branch", "a"), eq("branch", "b")), bin(expr.OpEq, ref("inputs", "env"), str("p")))
	e = and(e, bin(expr.OpEq, ref("needs", "build", "outputs", "ok"), str("y")))
	got, ok := tekton.GuardEnv(e)
	want := []tekton.EnvVar{
		{Name: "PM_CTX_BRANCH", Value: "$(params.ctx-branch)"},
		{Name: "PM_INPUT_ENV", Value: "$(params.env)"},
		{Name: "PM_NEEDS_BUILD__OK", Value: "$(tasks.build.results.ok)"},
	}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("GuardEnv = %#v, %v; want %#v", got, ok, want)
	}
	if got, ok := tekton.GuardEnv(eq("branch", "a")); !ok || len(got) != 1 {
		t.Errorf("GuardEnv one ref = %#v, %v", got, ok)
	}
	if _, ok := tekton.GuardEnv(bin(expr.OpEq, ref("env", "X"), str("y"))); ok {
		t.Error("GuardEnv accepted an env ref")
	}
	if got, ok := tekton.GuardEnv(bin(expr.OpEq, str("a"), str("a"))); ok {
		t.Errorf("GuardEnv on literals only = %#v, true", got)
	}
}

// TestGuardRunsInShell runs hostile literals through sh: the test must read the env value as data.
func TestGuardRunsInShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, lit := range []string{"main", "it's", "$(touch /nonexistent/x)", "`id`", `a "b" c`, "$HOME", "-n", "", "a b"} {
		line, err := tekton.PrintGuard(eq("branch", lit))
		if err != nil {
			t.Fatalf("%q: %v", lit, err)
		}
		cmd := exec.Command(sh, "-c", line+"\necho ran") //nolint:gosec // generated test script
		cmd.Env = []string{"PM_CTX_BRANCH=" + lit}
		out, err := cmd.Output()
		if err != nil || string(out) != "ran\n" {
			t.Errorf("%q: matching value: out %q err %v", lit, out, err)
		}
		cmd = exec.Command(sh, "-c", line+"\necho ran") //nolint:gosec // generated test script
		cmd.Env = []string{"PM_CTX_BRANCH=other"}
		out, err = cmd.Output()
		if err != nil || string(out) != "" {
			t.Errorf("%q: other value: out %q err %v", lit, out, err)
		}
	}
}
