//go:build behave

package behave

import "testing"

func TestCelRawStringAndMatches(t *testing.T) {
	body := map[string]any{"ref": "refs/heads/main"}
	tests := []struct {
		want any
		name string
		expr string
		bad  bool
	}{
		{want: true, name: "raw string equals", expr: `r'a\d' == 'a\d'`},
		{want: true, name: "anchored branch matches", expr: `body.ref.startsWith('refs/heads/') && (body.ref.replace('refs/heads/','',1).matches(r'^main$'))`},
		{want: false, name: "anchored branch differs", expr: `body.ref.replace('refs/heads/','',1).matches(r'^dev$')`},
		{want: true, name: "digits class", expr: `'v12'.matches(r'^v\d+$')`},
		{name: "identifier r is no raw string", expr: `r == 'x'`, bad: true},
		{name: "unterminated raw string", expr: `r'abc`, bad: true},
		{name: "bad regexp", expr: `'a'.matches('(')`, bad: true},
		{name: "matches needs one argument", expr: `'a'.matches()`, bad: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := celEval(tc.expr, body)
			if tc.bad {
				if err == nil {
					t.Fatalf("want an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
