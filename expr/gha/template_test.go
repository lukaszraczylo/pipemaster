package gha_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
)

func TestTemplate(t *testing.T) {
	parts, err := gha.ParseTemplate("echo ${{ github.sha }} done")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || parts[0].Text != "echo " || !equalExpr(parts[1].Expr, r("sha")) || parts[1].Text != "" ||
		parts[2].Text != " done" || parts[0].Expr != nil || parts[2].Expr != nil {
		t.Fatalf("parts = %#v", parts)
	}
}

func TestTemplateShapes(t *testing.T) {
	cases := []struct {
		src   string
		print string
		n     int
	}{
		{"plain text", "plain text", 1},
		{"", "", 0},
		{"${{ a }}", "${{ a }}", 1},
		{"${{a}}${{b}}", "${{ a }}${{ b }}", 2},
		{"x${{ a }}${{ b }}y", "x${{ a }}${{ b }}y", 4},
		{"${{   github.sha   }}!", "${{ github.sha }}!", 2},
		{"${{ '}}' }} tail", "${{ '}}' }} tail", 2},
		{"${{ fromJSON('{\"a\":{\"b\":1}}') == null }}", "${{ fromJSON('{\"a\":{\"b\":1}}') == null }}", 1},
		{"a $ b { c } ${{ x }}", "a $ b { c } ${{ x }}", 2},
		{"${{ a[b] }}", "${{ a[b] }}", 1},
	}
	for _, c := range cases {
		parts, err := gha.ParseTemplate(c.src)
		if err != nil || len(parts) != c.n {
			t.Errorf("%q: %d parts, %v want %d", c.src, len(parts), err, c.n)
			continue
		}
		out, err := gha.PrintTemplate(parts)
		if err != nil || out != c.print {
			t.Errorf("%q: PrintTemplate = %q, %v want %q", c.src, out, err, c.print)
			continue
		}
		again, err := gha.ParseTemplate(out)
		if err != nil || !equalParts(again, parts) {
			t.Errorf("%q: reparse differs: %#v, %v", c.src, again, err)
		}
	}
}

func equalParts(a, b []gha.Part) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Text != b[i].Text || !equalExpr(a[i].Expr, b[i].Expr) {
			return false
		}
	}
	return true
}

func TestTemplateErrors(t *testing.T) {
	for _, s := range []string{"a ${{ b", "${{", "${{ 'x }}", "${{ }}", "${{ a == }}", "x ${{ (a }}"} {
		if _, err := gha.ParseTemplate(s); err == nil {
			t.Errorf("%q: want error", s)
		}
	}
	if _, err := gha.ParseTemplate("a ${{ b"); err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Errorf("want unterminated error, got %v", err)
	}
}

func TestPrintTemplateErrors(t *testing.T) {
	bad := [][]gha.Part{
		{{Expr: r("branch")}},
		{{Text: "a ${{ b }}"}},
		{{Expr: r("sha"), Text: "x"}},
		{{Expr: expr.Opaque{Dialect: "gitlab", Text: "$X"}}},
	}
	for i, p := range bad {
		if _, err := gha.PrintTemplate(p); err == nil {
			t.Errorf("case %d: want error", i)
		}
	}
}
