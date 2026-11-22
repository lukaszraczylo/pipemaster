package argo_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/argo"
)

func TestRefExprAndTag(t *testing.T) {
	for _, c := range []struct {
		name  string
		expr  string
		tag   string
		path  []string
		scope argo.Scope
	}{
		{"context", "workflow.parameters['ctx-branch']", "{{workflow.parameters.ctx-branch}}", []string{"branch"}, argo.ScopeTask},
		{"dotted context", "workflow.parameters['ctx-pr-number']", "{{workflow.parameters.ctx-pr-number}}", []string{"pr", "number"}, argo.ScopeTask},
		{"underscore context", "workflow.parameters['ctx-ref-name']", "{{workflow.parameters.ctx-ref-name}}", []string{"ref_name"}, argo.ScopeTask},
		{"input", "workflow.parameters['tag']", "{{workflow.parameters.tag}}", []string{"inputs", "tag"}, argo.ScopeTask},
		{"needs output", "tasks['build'].outputs.parameters['digest']", "{{tasks.build.outputs.parameters.digest}}", []string{"needs", "build", "outputs", "digest"}, argo.ScopeTask},
		{"matrix in a task", "item['os']", "{{item.os}}", []string{"matrix", "os"}, argo.ScopeTask},
		{"matrix in the template", "inputs.parameters['os']", "{{inputs.parameters.os}}", []string{"matrix", "os"}, argo.ScopeTemplate},
		{"context in the template", "workflow.parameters['ctx-branch']", "{{workflow.parameters.ctx-branch}}", []string{"branch"}, argo.ScopeTemplate},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := expr.Ref{Path: c.path}
			if got, ok := argo.RefExpr(r, c.scope); !ok || got != c.expr {
				t.Errorf("RefExpr = %q, %v; want %q", got, ok, c.expr)
			}
			got, ok := argo.RefTag(r, c.scope)
			if !ok || got != c.tag {
				t.Fatalf("RefTag = %q, %v; want %q", got, ok, c.tag)
			}
			back, ok := argo.FromTag(got, c.scope)
			if !ok || !reflect.DeepEqual(back.Path, c.path) {
				t.Errorf("FromTag(%q) = %v, %v; want %v", got, back.Path, ok, c.path)
			}
		})
	}
}

func TestNoFormForSecretsEnvAndVars(t *testing.T) {
	for _, p := range [][]string{
		{"env", "X"}, {"secrets", "X"}, {"vars", "X"}, {"steps", "a", "outcome"}, {"nope"}, nil,
		{"inputs"}, {"inputs", "a", "b"}, {"inputs", "a b"}, {"inputs", "a'b"}, {"needs", "a"}, {"needs", "a", "outputs"},
		{"needs", "A", "outputs", "x"}, {"needs", "a", "result", "x"}, {"matrix", "a'b"},
	} {
		for _, s := range []argo.Scope{argo.ScopeTask, argo.ScopeTemplate} {
			if w, ok := argo.RefExpr(expr.Ref{Path: p}, s); ok {
				t.Errorf("RefExpr(%v) = %q; want a refusal", p, w)
			}
			if w, ok := argo.RefTag(expr.Ref{Path: p}, s); ok {
				t.Errorf("RefTag(%v) = %q; want a refusal", p, w)
			}
		}
	}
	// Tasks are not visible inside a template.
	needs := expr.Ref{Path: []string{"needs", "a", "outputs", "b"}}
	if w, ok := argo.RefTag(needs, argo.ScopeTemplate); ok {
		t.Errorf("RefTag(needs, template) = %q", w)
	}
}

// workflow.parameters.json is the list of all parameters, not a parameter named json.
func TestJSONInputHasNoTag(t *testing.T) {
	r := expr.Ref{Path: []string{"inputs", "json"}}
	if w, ok := argo.RefTag(r, argo.ScopeTask); ok {
		t.Errorf("RefTag(inputs.json) = %q", w)
	}
	if got, ok := argo.RefExpr(r, argo.ScopeTask); !ok || got != "workflow.parameters['json']" {
		t.Errorf("RefExpr(inputs.json) = %q, %v", got, ok)
	}
}

func TestFromTagRefusals(t *testing.T) {
	for _, w := range []string{
		"", "main", "{{workflow.parameters.a}} {{workflow.parameters.b}}", "{{workflow.parameters.}}", "{{workflow.parameters.a b}}",
		"{{workflow.name}}", "{{tasks.a.outputs.parameters.}}", "{{tasks.A.outputs.parameters.x}}", "{{workflow.parameters.a}}x",
		"x{{workflow.parameters.a}}", "{{workflow.parameters.a.b}}", "{{workflow.parameters['a']}}", "{{= workflow.parameters.a }}",
		"{{tasks.a.status}}", "{{tasks.a.outputs.result}}", "{{inputs.parameters.os}}", "{{workflow.parameters.json}}",
		"{{item}}", "{{item.a.b}}", "{{workflow.parameters.a'b}}",
	} {
		if r, ok := argo.FromTag(w, argo.ScopeTask); ok {
			t.Errorf("FromTag(%q) = %v; want a refusal", w, r)
		}
	}
	if r, ok := argo.FromTag("{{inputs.parameters.os}}", argo.ScopeTemplate); !ok || !reflect.DeepEqual(r.Path, []string{"matrix", "os"}) {
		t.Errorf("FromTag(template input) = %v, %v", r, ok)
	}
}
