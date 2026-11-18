package expr_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
)

func TestScriptTemplateShapes(t *testing.T) {
	cases := []struct {
		src   string
		print string
		n     int
	}{
		{"plain text", "plain text", 1},
		{"", "", 0},
		{"echo ${{ sha }}", "echo ${{ sha }}", 2},
		{"echo ${{sha}} x", "echo ${{ sha }} x", 3},
		{"${{ '${{' }}", "${{ '${{' }}", 1},
		{"a ${{ '${{' }} b", "a ${{ '${{' }} b", 3},
		{"${{ opaque(gha): github.event.x[a] }}", "${{ opaque(gha): github.event.x[a] }}", 1},
		{"${{ gha.github.workspace }}/x", "${{ gha.github.workspace }}/x", 2},
		{"${{ '}}' }} tail", "${{ '}}' }} tail", 2},
	}
	for _, c := range cases {
		parts, err := expr.ParseScriptTemplate(c.src)
		if err != nil || len(parts) != c.n {
			t.Errorf("%q: %d parts, %v want %d", c.src, len(parts), err, c.n)
			continue
		}
		out, err := expr.PrintScriptTemplate(parts)
		if err != nil || out != c.print {
			t.Errorf("%q: Print = %q, %v want %q", c.src, out, err, c.print)
			continue
		}
		again, err := expr.ParseScriptTemplate(out)
		if err != nil || !reflect.DeepEqual(again, parts) {
			t.Errorf("%q: reparse differs: %#v, %v", c.src, again, err)
		}
	}
}

func TestPrintScriptTemplateEscapesLiteralOpen(t *testing.T) {
	out, err := expr.PrintScriptTemplate([]expr.Part{{Text: "echo "}, {Text: "${{ x }}"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "echo ${{ '${{' }} x }}"; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestScriptTemplateSplitDelimiterAcrossParts(t *testing.T) {
	parts := []expr.Part{{Text: "$"}, {Text: "{{ a }}"}}
	out, err := expr.PrintScriptTemplate(parts)
	if err != nil {
		t.Fatal(err)
	}
	if want := "${{ '${{' }} a }}"; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestScriptTemplateErrors(t *testing.T) {
	for _, s := range []string{"a ${{ b", "${{", "${{ 'x }}", "${{ }}", "${{ a == }}"} {
		if _, err := expr.ParseScriptTemplate(s); err == nil {
			t.Errorf("%q: want error", s)
		}
	}
	bad := [][]expr.Part{
		{{Expr: expr.Ref{Path: []string{"sha"}}, Text: "x"}},
		{{Expr: expr.Call{Name: "opaque"}}},
	}
	for i, p := range bad {
		if _, err := expr.PrintScriptTemplate(p); err == nil {
			t.Errorf("case %d: want error", i)
		}
	}
}
