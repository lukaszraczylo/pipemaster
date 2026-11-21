package container

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

type fakeMap struct {
	vals map[string]any
	keys []string
}

func (m fakeMap) Keys() []string   { return m.keys }
func (m fakeMap) Get(k string) any { return m.vals[k] }
func newMap(kv ...any) fakeMap {
	m := fakeMap{vals: map[string]any{}}
	for len(kv) >= 2 {
		k, _ := kv[0].(string)
		m.keys = append(m.keys, k)
		m.vals[k] = kv[1]
		kv = kv[2:]
	}
	return m
}

type fakeDec struct{}

func (fakeDec) Text(v any) (string, bool) { s, ok := v.(string); return s, ok }
func (d fakeDec) TextList(v any) ([]string, bool) {
	if s, ok := v.(string); ok {
		return []string{s}, true
	}
	l, ok := v.([]string)
	return l, ok
}
func (fakeDec) Mapping(v any) (Mapping, bool) { m, ok := v.(fakeMap); return m, ok }
func (fakeDec) Template(s string) (string, error) {
	if s == "bad" {
		return "", errors.New("bad template")
	}
	return s, nil
}
func (fakeDec) Value(s string) ir.Value { return ir.Value{Lit: s} }

func takeFrom(m fakeMap) Take {
	return func(k string) (any, bool) { v, ok := m.vals[k]; return v, ok }
}

func TestEscape(t *testing.T) {
	for _, form := range []string{"$$", "\\$"} {
		in := "a$b${c}"
		got := Escape(in, form)
		if got == in || Unescape(got, form) != in {
			t.Errorf("form %q: Escape=%q does not round-trip", form, got)
		}
	}
	if got := Escape("$x", "$$"); got != "$$x" {
		t.Errorf("Escape = %q, want $$x", got)
	}
	if got := Unescape("$$x", "$$"); got != "$x" {
		t.Errorf("Unescape = %q, want $x", got)
	}
}

func TestSanitizeID(t *testing.T) {
	tests := []struct{ id, extra, want string }{
		{"build test", "", "build-test"},
		{"a.b_c", "_.", "a.b_c"},
		{"a.b_c", "", "a-b-c"},
		{"", "", ""},
	}
	for _, tc := range tests {
		if got := SanitizeID(tc.id, tc.extra); got != tc.want {
			t.Errorf("SanitizeID(%q,%q) = %q, want %q", tc.id, tc.extra, got, tc.want)
		}
	}
}

func TestReadValue(t *testing.T) {
	sec := newMap(FromSecret, "tok")
	tests := []struct {
		name string
		in   any
		want ir.Value
		ok   bool
	}{
		{"scalar", "x", ir.Value{Lit: "x"}, true},
		{"secret", sec, ir.Value{Kind: ir.ValueSecret, Secret: "tok"}, true},
		{"two keys", newMap(FromSecret, "tok", "other", "y"), ir.Value{}, false},
		{"list", []string{"a"}, ir.Value{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ReadValue(fakeDec{}, tc.in)
			if ok != tc.ok || got != tc.want {
				t.Errorf("got %v %v, want %v %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestEnvAndSettings(t *testing.T) {
	m := newMap("A", "1", "S", newMap(FromSecret, "s"), "L", []string{"x"})
	vars, left := Env(fakeDec{}, m)
	if len(vars) != 2 || vars[0].Name != "A" || vars[1].Value.Secret != "s" || !reflect.DeepEqual(left, []string{"L"}) {
		t.Errorf("Env = %v %v", vars, left)
	}
	with, left := Settings(fakeDec{}, m, false)
	if len(with) != 2 || !reflect.DeepEqual(left, []string{"L"}) {
		t.Errorf("Settings = %v %v", with, left)
	}
	with, left = Settings(fakeDec{}, m, true)
	if with != nil || len(left) != 3 {
		t.Errorf("script Settings = %v %v, want all left", with, left)
	}
}

func TestReadImage(t *testing.T) {
	ok := newMap("image", "alpine", "entrypoint", []string{"sh", "-c"})
	img, err := ReadImage(fakeDec{}, "step s", takeFrom(ok))
	if err != nil || img.Ref != "alpine" || !reflect.DeepEqual(img.Entrypoint, []string{"sh", "-c"}) {
		t.Errorf("ReadImage = %+v %v", img, err)
	}
	if _, err := ReadImage(fakeDec{}, "step s", takeFrom(newMap())); err == nil || err.Error() != "step s: image is required" {
		t.Errorf("missing image error = %v", err)
	}
	if _, err := ReadImage(fakeDec{}, "step s", takeFrom(newMap("image", "alpine", "entrypoint", 3))); err == nil {
		t.Error("a bad entrypoint must fail")
	}
	if _, err := ReadImage(fakeDec{}, "step s", takeFrom(newMap("image", "bad"))); err == nil {
		t.Error("a template error must fail")
	}
}

func TestService(t *testing.T) {
	m := newMap("name", "db", "image", "pg", "ports", []string{"5432$$"},
		"environment", newMap("A", "1", "L", []string{"x"}))
	sv, left, err := Service(fakeDec{}, "db", "$$", takeFrom(m))
	if err != nil {
		t.Fatal(err)
	}
	if sv.Name != "db" || sv.Image.Ref != "pg" || !reflect.DeepEqual(sv.Ports, []string{"5432$"}) ||
		len(sv.Env) != 1 || !reflect.DeepEqual(left, []string{"L"}) {
		t.Errorf("Service = %+v left %v", sv, left)
	}
	for _, bad := range []fakeMap{
		newMap("name", "db"),
		newMap("image", "pg", "ports", 1),
		newMap("image", "pg", "environment", "x"),
	} {
		if _, _, err := Service(fakeDec{}, "db", "$$", takeFrom(bad)); err == nil {
			t.Errorf("Service(%v) must fail", bad.vals)
		}
	}
}
