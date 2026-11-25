package argo_test

import (
	"errors"
	"github.com/lukaszraczylo/pipemaster/expr/internal/shguard"
	"os/exec"
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/argo"
)

func TestPrintGuard(t *testing.T) {
	for _, c := range []struct {
		e    expr.Expr
		name string
		want string
	}{
		{eq("branch", "main"), "context ref", `if ! [ "$PM_CTX_BRANCH" = 'main' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`},
		{bin(expr.OpNe, ref("pr", "number"), str("1")), "dotted context name", `if ! [ "$PM_CTX_PR_NUMBER" != '1' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`},
		{eq("ref_name", "x"), "underscore context name", `if ! [ "$PM_CTX_REF_NAME" = 'x' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`},
		{bin(expr.OpEq, ref("inputs", "deploy-env"), str("prod")), "input", `if ! [ "$PM_INPUT_DEPLOY_ENV" = 'prod' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`},
		{bin(expr.OpEq, ref("matrix", "os"), str("linux")), "matrix axis", `if ! [ "$PM_MATRIX_OS" = 'linux' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`},
		{eq("branch", "it's"), "single quote", `if ! [ "$PM_CTX_BRANCH" = 'it'\''s' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`},
		{eq("branch", "{ x }"), "single braces", `if ! [ "$PM_CTX_BRANCH" = '{ x }' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`},
		{or(and(eq("branch", "a"), eq("tag", "b")), ne("repo", "c")), "mixed operators group",
			`if ! ( ( [ "$PM_CTX_BRANCH" = 'a' ] && [ "$PM_CTX_TAG" = 'b' ] ) || [ "$PM_CTX_REPO" != 'c' ] ); then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`},
		{not(eq("branch", "a")), "not", `if ! ( ! [ "$PM_CTX_BRANCH" = 'a' ] ); then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := argo.PrintGuard(c.e)
			if err != nil || got != c.want {
				t.Fatalf("PrintGuard =\n%s\nerr %v; want\n%s", got, err, c.want)
			}
			back, ok := argo.ParseGuard(got)
			if !ok || !reflect.DeepEqual(back, c.e) {
				t.Errorf("ParseGuard = %#v, %v; want %#v", back, ok, c.e)
			}
		})
	}
}

func TestNotGuardable(t *testing.T) {
	for name, e := range map[string]expr.Expr{
		"call":             expr.Call{Name: "success"},
		"opaque":           expr.Opaque{Dialect: argo.Dialect, Text: "{{= x }}"},
		"bare ref":         ref("branch"),
		"ordering":         bin(expr.OpLt, ref("branch"), str("x")),
		"number literal":   bin(expr.OpEq, ref("branch"), num(1)),
		"newline literal":  eq("branch", "a\nb"),
		"nul literal":      eq("branch", "a\x00b"),
		"env ref":          bin(expr.OpEq, ref("env", "X"), str("y")),
		"secret ref":       bin(expr.OpEq, ref("secrets", "X"), str("y")),
		"unknown context":  eq("workflow", "a"),
		"uppercase input":  bin(expr.OpEq, ref("inputs", "Env"), str("y")),
		"underscore input": bin(expr.OpEq, ref("inputs", "my_env"), str("y")),
		"double dash":      bin(expr.OpEq, ref("inputs", "a--b"), str("y")),
		"needs output":     bin(expr.OpEq, ref("needs", "a", "outputs", "b"), str("y")),
	} {
		if argo.Guardable(e) {
			t.Errorf("%s: Guardable = true", name)
		}
		if line, err := argo.PrintGuard(e); !errors.Is(err, argo.ErrNotGuardable) {
			t.Errorf("%s: PrintGuard = %q, %v; want ErrNotGuardable", name, line, err)
		}
	}
}

// Argo substitutes every {{...}} in container args and script text with no escaping, so a literal that holds
// one would be rewritten before the shell runs. Such a literal is refused, never quoted.
func TestGuardRefusesTemplateTagInLiteral(t *testing.T) {
	for _, lit := range []string{
		"{{workflow.parameters.ctx-branch}}", "x{{workflow.name}}", "{{", "a{{b", "it's {{= 1 }}", "{{inputs.parameters.x}}",
	} {
		e := eq("branch", lit)
		if argo.Guardable(e) {
			t.Errorf("%q: Guardable = true", lit)
		}
		if line, err := argo.PrintGuard(e); !errors.Is(err, argo.ErrNotGuardable) {
			t.Errorf("%q: PrintGuard = %q, %v; want ErrNotGuardable", lit, line, err)
		}
		if _, err := argo.AddGuard([]string{"x"}, e); !errors.Is(err, argo.ErrNotGuardable) {
			t.Errorf("%q: AddGuard = %v", lit, err)
		}
	}
	// A lone brace, a close tag and shell text carry no Argo tag start and stay guardable.
	for _, lit := range []string{"{", "}}", "{ {", "$HOME", "${x}", "$(x)", "a}"} {
		if !argo.Guardable(eq("branch", lit)) {
			t.Errorf("%q: Guardable = false", lit)
		}
	}
}

func TestParseGuardRejectsOtherText(t *testing.T) {
	good, err := argo.PrintGuard(eq("branch", "main"))
	if err != nil {
		t.Fatal(err)
	}
	for name, line := range map[string]string{
		"empty":           "",
		"plain script":    "echo hi",
		"no marker":       `if ! [ "$PM_CTX_BRANCH" = 'main' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi`,
		"trailing text":   good + " && rm -rf /",
		"unknown env":     `if ! [ "$HOME" = 'main' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`,
		"unknown context": `if ! [ "$PM_CTX_NOPE" = 'main' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`,
		"needs env":       `if ! [ "$PM_NEEDS_A__B" = 'main' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`,
		"template tag":    `if ! [ "$PM_CTX_BRANCH" = '{{workflow.name}}' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`,
		"lowercase input": `if ! [ "$PM_INPUT_a" = 'x' ]; then echo 'pipemaster: skipped: the condition of this step is false'; exit 0; fi # pipemaster:guard`,
	} {
		if e, ok := argo.ParseGuard(line); ok {
			t.Errorf("%s: ParseGuard accepted %q as %#v", name, line, e)
		}
	}
}

func TestAddRemoveGuard(t *testing.T) {
	script := []string{"echo hi", "make"}
	e := eq("branch", "main")
	guarded, err := argo.AddGuard(script, e)
	if err != nil || len(guarded) != 3 || guarded[1] != script[0] {
		t.Fatalf("AddGuard = %q, %v", guarded, err)
	}
	if len(script) != 2 {
		t.Error("AddGuard changed its input")
	}
	rest, got, ok := argo.RemoveGuard(guarded)
	if !ok || !reflect.DeepEqual(rest, script) || !reflect.DeepEqual(got, e) {
		t.Errorf("RemoveGuard = %q, %#v, %v", rest, got, ok)
	}
	if rest, _, ok := argo.RemoveGuard(script); ok || !reflect.DeepEqual(rest, script) {
		t.Errorf("RemoveGuard on a plain script = %q, %v", rest, ok)
	}
	if _, _, ok := argo.RemoveGuard(nil); ok {
		t.Error("RemoveGuard on no script")
	}
}

func TestGuardEnv(t *testing.T) {
	e := and(or(eq("branch", "a"), eq("branch", "b")), bin(expr.OpEq, ref("inputs", "env"), str("p")))
	e = and(e, bin(expr.OpEq, ref("matrix", "os"), str("linux")))
	got, ok := argo.GuardEnv(e)
	want := []argo.EnvVar{
		{Name: "PM_CTX_BRANCH", Value: "{{workflow.parameters.ctx-branch}}"},
		{Name: "PM_INPUT_ENV", Value: "{{workflow.parameters.env}}"},
		{Name: "PM_MATRIX_OS", Value: "{{inputs.parameters.os}}"},
	}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("GuardEnv = %#v, %v; want %#v", got, ok, want)
	}
	for name, e := range map[string]expr.Expr{
		"env ref":       bin(expr.OpEq, ref("env", "X"), str("y")),
		"needs ref":     bin(expr.OpEq, ref("needs", "a", "outputs", "b"), str("y")),
		"literals only": bin(expr.OpEq, str("a"), str("a")),
	} {
		if got, ok := argo.GuardEnv(e); ok {
			t.Errorf("%s: GuardEnv = %#v, true", name, got)
		}
	}
}

func TestGuardEnvNamesInvert(t *testing.T) {
	for _, p := range [][]string{{"branch"}, {"pr", "head_ref"}, {"inputs", "a-b"}, {"matrix", "os"}} {
		name, ok := argo.EnvName(expr.Ref{Path: p})
		if !ok {
			t.Fatalf("EnvName(%v) refused", p)
		}
		if back, ok := argo.EnvRef(name); !ok || !reflect.DeepEqual(back.Path, p) {
			t.Errorf("EnvRef(%q) = %v, %v; want %v", name, back, ok, p)
		}
	}
}

// TestGuardRunsInShell runs hostile literals through sh: the test must read the env value as data.
func TestGuardRunsInShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, lit := range []string{"main", "it's", "${x}", "`id`", `a "b" c`, "$HOME", "-n", "", "a b", "}}", "{"} {
		line, err := argo.PrintGuard(eq("branch", lit))
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
		if err != nil || string(out) != shguard.SkipMessage+"\n" {
			t.Errorf("%q: other value: out %q err %v", lit, out, err)
		}
	}
}
