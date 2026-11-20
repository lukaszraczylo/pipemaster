package expr_test

import (
	"sync"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
)

const fakeID = "fakegl"

var fakeOnce sync.Once

// fakeGL is a second ClassGitLab dialect that names branch $FAKE_BRANCH and has no tag name.
func fakeGL() expr.Dialect {
	fakeOnce.Do(func() {
		expr.RegisterLang(&expr.Dialect{
			ID:     fakeID,
			Class:  expr.ClassGitLab,
			Sem:    gitlab.Semantics,
			Events: gitlab.Events,
			Vocab: expr.Vocab{
				ToCanon: map[string][]string{
					"FAKE_BRANCH": {expr.CtxBranch}, "FAKE_REF": {expr.CtxRefName},
					"FAKE_SHA": {expr.CtxSHA}, "FAKE_EVENT": {expr.CtxEvent},
				},
				FromCanon: map[string][]string{
					expr.CtxBranch: {"FAKE_BRANCH"}, expr.CtxRefName: {"FAKE_REF"},
					expr.CtxSHA: {"FAKE_SHA"}, expr.CtxEvent: {"FAKE_EVENT"},
				},
			},
		})
	})
	d, ok := expr.LangOf(fakeID)
	if !ok {
		panic("fakegl not registered")
	}
	return *d
}

// printFake prints e the way gitlab.Print does, with refs named by the dialect's vocabulary.
func printFake(t *testing.T, d expr.Dialect, e expr.Expr) string {
	t.Helper()
	var rename func(expr.Expr) expr.Expr
	rename = func(x expr.Expr) expr.Expr {
		switch v := x.(type) {
		case expr.Ref:
			p, ok := d.Vocab.Dialect(v.Path)
			if !ok {
				t.Fatalf("%s names no %v", d.ID, v.Path)
			}
			return expr.Ref{Path: []string{gitlab.Dialect, p[0]}}
		case expr.Unary:
			return expr.Unary{Op: v.Op, X: rename(v.X)}
		case expr.Binary:
			return expr.Binary{Op: v.Op, L: rename(v.L), R: rename(v.R)}
		}
		return x
	}
	s, err := gitlab.Print(rename(e))
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	return s
}

func ghaText(t *testing.T, e expr.Expr) string {
	t.Helper()
	s, err := gha.Print(e)
	if err != nil {
		t.Fatalf("gha.Print: %v", err)
	}
	return s
}

func TestRegisteredLangs(t *testing.T) {
	for id, class := range map[string]expr.Class{
		"gha": expr.ClassGHA, "gitea": expr.ClassGHA, "forgejo": expr.ClassGHA, "gitlab": expr.ClassGitLab,
	} {
		d, ok := expr.LangOf(id)
		if !ok || d.Class != class || d.ID != id {
			t.Errorf("LangOf(%q) = %+v, %v; want class %d", id, d, ok, class)
		}
	}
	if _, ok := expr.LangOf("nope"); ok {
		t.Error("LangOf(nope) found a dialect")
	}
}

func TestRegisterLangRefusesBadInput(t *testing.T) {
	fakeGL()
	for name, d := range map[string]*expr.Dialect{
		"nil": nil, "no id": {Class: expr.ClassGHA}, "no class": {ID: "classless"}, "duplicate": {ID: fakeID, Class: expr.ClassGitLab},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: RegisterLang did not panic", name)
				}
			}()
			expr.RegisterLang(d)
		}()
	}
}

func TestTranslateAcrossClassesUsesTargetVocabulary(t *testing.T) {
	fake := fakeGL()
	out, ok := expr.Translate(ghaParse(t, `github.ref_type == 'branch' && github.ref_name == 'main'`), gha.Lang, fake)
	if !ok {
		t.Fatal("gha to fakegl refused")
	}
	if got, want := printFake(t, fake, out), `$FAKE_BRANCH =~ /^main$/i`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if out, ok := expr.Translate(ghaParse(t, `github.ref_name == 'main'`), gha.Lang, fake); !ok || printFake(t, fake, out) != `$FAKE_REF =~ /^main$/i` {
		t.Errorf("ref_name: %v %v", out, ok)
	}
	// Canonical branch has no GHA name, so GHA cannot state a bare branch test.
	if _, ok := expr.Translate(expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxBranch}}, R: expr.Lit{Kind: expr.LitStr, Str: "main"}}, gha.Lang, fake); ok {
		t.Error("bare branch == 'main' translated")
	}
}

func TestTranslateFromOtherGitLabClass(t *testing.T) {
	fake := fakeGL()
	e := expr.Binary{Op: expr.OpMatch, L: expr.Ref{Path: []string{expr.CtxBranch}}, R: expr.Lit{Kind: expr.LitStr, Str: "(?i)^main$"}}
	out, ok := expr.Translate(e, fake, gha.Lang)
	if !ok {
		t.Fatal("fakegl to gha refused")
	}
	if got, want := ghaText(t, out), `github.ref_type == 'branch' && github.ref_name == 'main'`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, cav, ok := expr.TranslateApprox(expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxRefName}}, R: expr.Lit{Kind: expr.LitStr, Str: "main"}}, fake, gha.Lang); !ok || len(cav) == 0 {
		t.Errorf("TranslateApprox from fakegl: ok=%v caveats=%v", ok, cav)
	}
	if _, _, ok := expr.TranslateApprox(ghaParse(t, `github.ref_name == github.sha`), gha.Lang, fake); ok {
		t.Error("TranslateApprox must keep to ClassGitLab to ClassGHA")
	}
}

func TestTranslateRefusesRefTheVocabularyCannotName(t *testing.T) {
	fake := fakeGL()
	tag := expr.Binary{Op: expr.OpMatch, L: expr.Ref{Path: []string{expr.CtxTag}}, R: expr.Lit{Kind: expr.LitStr, Str: "(?i)^v1$"}}
	for name, c := range map[string]struct{ from, to expr.Dialect }{
		"source names no tag": {fake, gha.Lang},
		"target names no tag": {gitlab.Lang, fake},
	} {
		if _, ok := expr.Translate(tag, c.from, c.to); ok {
			t.Errorf("%s: translated", name)
		}
		if expr.Translatable(tag, c.from, c.to) {
			t.Errorf("%s: Translatable", name)
		}
	}
	guard := ghaParse(t, `github.ref_type == 'tag' && github.ref_name == 'v1'`)
	if _, ok := expr.Translate(guard, gha.Lang, fake); ok {
		t.Error("tag test translated to a dialect with no tag name")
	}
	if expr.Translatable(guard, gha.Lang, fake) {
		t.Error("Translatable disagrees with Translate")
	}
}

func TestTranslateSameClassRenames(t *testing.T) {
	fake := fakeGL()
	gitea, _ := expr.LangOf("gitea")
	e := ghaParse(t, `github.ref_name == 'main'`)
	if out, ok := expr.Translate(e, gha.Lang, *gitea); !ok || expr.String(out) != expr.String(e) {
		t.Errorf("gha to gitea: %v %v", out, ok)
	}
	branch := expr.Binary{Op: expr.OpMatch, L: expr.Ref{Path: []string{expr.CtxBranch}}, R: expr.Lit{Kind: expr.LitStr, Str: "(?i)^main$"}}
	out, ok := expr.Translate(branch, gitlab.Lang, fake)
	if !ok || printFake(t, fake, out) != `$FAKE_BRANCH =~ /^main$/i` {
		t.Errorf("gitlab to fakegl: %v %v", out, ok)
	}
	for name, x := range map[string]expr.Expr{
		"unnamed ref":  expr.Ref{Path: []string{expr.CtxTag}},
		"native ref":   expr.Ref{Path: []string{gitlab.Dialect, "CI_JOB_NAME"}},
		"opaque":       expr.Opaque{Dialect: gitlab.Dialect, Text: "x"},
		"nested ref":   expr.Binary{Op: expr.OpAnd, L: branch, R: expr.Ref{Path: []string{expr.CtxTag}}},
		"unmapped ctx": expr.Ref{Path: []string{expr.CtxActor}},
	} {
		if _, ok := expr.Translate(x, gitlab.Lang, fake); ok {
			t.Errorf("%s translated", name)
		}
		if expr.Translatable(x, gitlab.Lang, fake) {
			t.Errorf("%s: Translatable", name)
		}
	}
	if _, ok := expr.Translate(e, gha.Lang, expr.Dialect{ID: "classless"}); ok {
		t.Error("translated to a dialect without a class")
	}
}

func TestSourceFor(t *testing.T) {
	fakeGL()
	for _, c := range []struct {
		target string
		origin string
		want   string // "" means nil
	}{
		{"gha", "", ""}, {"gha", "gha", ""}, {"gha", "gitea", ""}, {"gha", "forgejo", ""},
		{"gha", "gitlab", "gitlab"}, {"gitlab", "gha", "gha"}, {"gitlab", "gitea", "gitea"},
		{"gha", fakeID, fakeID}, {"gitlab", fakeID, fakeID}, {"gha", "drone", "drone"},
	} {
		target, _ := expr.LangOf(c.target)
		got := expr.SourceFor(c.origin, *target)
		switch {
		case c.want == "" && got != nil, c.want != "" && (got == nil || got.ID != c.want):
			t.Errorf("SourceFor(%q, %s) = %+v, want %q", c.origin, c.target, got, c.want)
		}
	}
	if d := expr.SourceFor("drone", gha.Lang); d.Class != 0 {
		t.Errorf("unregistered origin has class %d", d.Class)
	}

}

var evOnce sync.Once

// eventDialects registers two same-class dialects whose Events tables differ from gitlab's and gha's.
func eventDialects() (gl, gh expr.Dialect) {
	evOnce.Do(func() {
		expr.RegisterLang(&expr.Dialect{
			ID: "fakeev", Class: expr.ClassGitLab, Sem: gitlab.Semantics, Vocab: gitlab.Vocab,
			Events: map[string]string{"push": "push", "pull_request": "mr", "schedule": "cron", "workflow_dispatch": "web"},
		})
		expr.RegisterLang(&expr.Dialect{
			ID: "fakeevgha", Class: expr.ClassGHA, Sem: gha.Semantics, Vocab: gha.Vocab,
			Events: map[string]string{"push": "Push", "pull_request": "PR", "schedule": "cron", "workflow_dispatch": "dispatch"},
		})
	})
	a, _ := expr.LangOf("fakeev")
	b, _ := expr.LangOf("fakeevgha")
	return *a, *b
}

func evRef() expr.Expr { return expr.Ref{Path: []string{expr.CtxEvent}} }

func TestSameClassMapsEventLiterals(t *testing.T) {
	fgl, fgh := eventDialects()
	for _, c := range []struct {
		from, to *expr.Dialect
		in, want expr.Expr
		name     string
	}{
		{&gitlab.Lang, &fgl, expr.Binary{Op: expr.OpEq, L: evRef(), R: str("merge_request_event")}, expr.Binary{Op: expr.OpEq, L: evRef(), R: str("mr")}, "gitlab eq"},
		{&gitlab.Lang, &fgl, expr.Binary{Op: expr.OpNe, L: str("web"), R: evRef()}, expr.Binary{Op: expr.OpNe, L: str("web"), R: evRef()}, "gitlab ne, literal first"},
		{&gitlab.Lang, &fgl, expr.Binary{Op: expr.OpMatch, L: evRef(), R: str("(?i)^merge_request_event$")}, expr.Binary{Op: expr.OpMatch, L: evRef(), R: str("(?i)^mr$")}, "gitlab regex"},
		{&gitlab.Lang, &fgl,
			expr.Binary{Op: expr.OpAnd, L: expr.Binary{Op: expr.OpEq, L: evRef(), R: str("schedule")}, R: expr.Binary{Op: expr.OpNoMatch, L: evRef(), R: str("(?i)^web$")}},
			expr.Binary{Op: expr.OpAnd, L: expr.Binary{Op: expr.OpEq, L: evRef(), R: str("cron")}, R: expr.Binary{Op: expr.OpNoMatch, L: evRef(), R: str("(?i)^web$")}},
			"gitlab and"},
		{&gha.Lang, &fgh, expr.Binary{Op: expr.OpEq, L: evRef(), R: str("Pull_Request")}, expr.Binary{Op: expr.OpEq, L: evRef(), R: str("PR")}, "gha eq ignores case"},
		{&fgh, &gha.Lang, expr.Binary{Op: expr.OpEq, L: evRef(), R: str("pr")}, expr.Binary{Op: expr.OpEq, L: evRef(), R: str("pull_request")}, "gha back"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := expr.Translate(c.in, *c.from, *c.to)
			if !ok || expr.String(got) != expr.String(c.want) {
				t.Fatalf("Translate = %s, %v; want %s", expr.String(got), ok, expr.String(c.want))
			}
			if !expr.Translatable(c.in, *c.from, *c.to) {
				t.Error("Translatable disagrees with Translate")
			}
		})
	}
}

func TestSameClassRefusesUnmappableEvents(t *testing.T) {
	fgl, fgh := eventDialects()
	short := fgl
	short.Events = map[string]string{"push": "push"}
	dup := fgl
	dup.Events = map[string]string{"push": "x", "pull_request": "X", "schedule": "cron", "workflow_dispatch": "web"}
	for _, c := range []struct {
		from, to *expr.Dialect
		in       expr.Expr
		name     string
	}{
		{&gitlab.Lang, &fgl, expr.Binary{Op: expr.OpEq, L: evRef(), R: str("mr")}, "literal not in source table"},
		{&gitlab.Lang, &fgl, expr.Binary{Op: expr.OpEq, L: evRef(), R: str("PUSH")}, "gitlab compares case-sensitively"},
		{&gitlab.Lang, &short, expr.Binary{Op: expr.OpEq, L: evRef(), R: str("web")}, "target table lacks the event"},
		{&gitlab.Lang, &dup, expr.Binary{Op: expr.OpEq, L: evRef(), R: str("push")}, "target table not injective"},
		{&gitlab.Lang, &fgl, expr.Binary{Op: expr.OpAnd, L: evRef(), R: expr.Binary{Op: expr.OpEq, L: evRef(), R: str("push")}}, "bare event ref"},
		{&gitlab.Lang, &fgl, expr.Binary{Op: expr.OpMatch, L: evRef(), R: str("(?i)^merge")}, "prefix pattern"},
		{&gha.Lang, &fgh, expr.Binary{Op: expr.OpEq, L: evRef(), R: expr.Ref{Path: []string{expr.CtxSHA}}}, "event against a ref"},
		{&gha.Lang, &fgh, expr.Call{Name: "startsWith", Args: []expr.Expr{evRef(), str("pu")}}, "event in a call"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if out, ok := expr.Translate(c.in, *c.from, *c.to); ok {
				t.Fatalf("Translate = %s, want refusal", expr.String(out))
			}
			if expr.Translatable(c.in, *c.from, *c.to) {
				t.Error("Translatable disagrees with Translate")
			}
		})
	}
}

func TestSameClassEqualEventTablesKeepEveryEventUse(t *testing.T) {
	fake := fakeGL()
	e := expr.Binary{Op: expr.OpAnd, L: evRef(), R: expr.Call{Name: "startsWith", Args: []expr.Expr{evRef(), str("pu")}}}
	if out, ok := expr.Translate(e, gitlab.Lang, fake); !ok || expr.String(out) != expr.String(e) {
		t.Errorf("equal tables: %v %v", out, ok)
	}
}

func TestSameClassGHAHasNoEventPattern(t *testing.T) {
	_, fgh := eventDialects()
	e := expr.Binary{Op: expr.OpMatch, L: evRef(), R: str("(?i)^push$")}
	if _, ok := expr.Translate(e, gha.Lang, fgh); ok {
		t.Error("a GHA =~ node has no model and must not translate across different Events tables")
	}
}
