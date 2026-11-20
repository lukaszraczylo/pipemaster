package validate

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/validate/schemas"
)

func TestOnKeyStaysString(t *testing.T) {
	v, err := toJSONValue([]byte("on: push\n"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("got %T", v)
	}
	if _, ok := m["on"]; !ok {
		t.Fatalf("no string key on: %v", m)
	}
}

func TestNonStringKeyRejected(t *testing.T) {
	if _, err := toJSONValue([]byte("{[a]: 1}\n")); err == nil {
		t.Fatal("want error for non-string key")
	}
}

func TestIntKeyConverted(t *testing.T) {
	v, err := toJSONValue([]byte("1: a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["1"] != "a" {
		t.Fatalf("got %v", v)
	}
}

func TestOfflineLoaderRefusesExternalRef(t *testing.T) {
	_, err := compileDoc("https://example.test/s.json", []byte(`{"$ref":"https://example.com/x.json"}`))
	if err == nil || !strings.Contains(err.Error(), "network access refused") {
		t.Fatalf("got %v", err)
	}
}

func TestSchemaCompiledOnce(t *testing.T) {
	Register("compile-once", schemas.GitHubWorkflow, nil)
	a, err := schemaFor("compile-once")
	if err != nil {
		t.Fatal(err)
	}
	b, err := schemaFor("compile-once")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("schema recompiled")
	}
}

func TestRegister(t *testing.T) {
	const doc = `{"$id":"https://example.test/s.json","type":"object"}`
	panics := func(f func()) (p bool) {
		defer func() { p = recover() != nil }()
		f()
		return false
	}
	cases := []struct {
		run  func()
		name string
		want bool
	}{
		{func() { Register("reg-ok", []byte(doc), []string{"x/*.yml"}) }, "valid schema", false},
		{func() { Register("reg-ok", []byte(doc), nil) }, "duplicate id", true},
		{func() { Register("reg-bad", []byte("{"), nil) }, "not JSON", true},
		{func() { Register("reg-noid", []byte(`{}`), nil) }, "no $id", true},
	}
	for _, c := range cases {
		if got := panics(c.run); got != c.want {
			t.Errorf("%s: panic = %v, want %v", c.name, got, c.want)
		}
	}
	if err := File("reg-ok", "a.yml", []byte("a: 1\n")); err != nil {
		t.Errorf("registered schema rejects a valid file: %v", err)
	}
	if got := Patterns("reg-ok"); len(got) != 1 || got[0] != "x/*.yml" {
		t.Errorf("patterns = %v", got)
	}
}

func TestRegisterSharesIdenticalSchemas(t *testing.T) {
	Register("share-a", schemas.GitLabCI, nil)
	Register("share-b", schemas.GitLabCI, nil)
	a, errA := schemaFor("share-a")
	b, errB := schemaFor("share-b")
	if errA != nil || errB != nil || a != b {
		t.Fatalf("identical schema bytes compiled twice: %v %v", errA, errB)
	}
}
