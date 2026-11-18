package validate

import (
	"errors"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider"
)

func TestValidGHA(t *testing.T) {
	y := "name: ci\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"
	if err := File(provider.GHA, "ci.yml", []byte(y)); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidGHA(t *testing.T) {
	y := "on: push\njobs:\n  build:\n    steps:\n      - run: echo hi\n"
	err := File(provider.GHA, "ci.yml", []byte(y))
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "runs-on") || !strings.Contains(err.Error(), "ci.yml") {
		t.Fatalf("error lacks runs-on or file name: %v", err)
	}
}

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

func TestValidGitLab(t *testing.T) {
	y := "build:\n  script:\n    - echo hi\n"
	if err := File(provider.GitLab, ".gitlab-ci.yml", []byte(y)); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidGitLab(t *testing.T) {
	err := File(provider.GitLab, ".gitlab-ci.yml", []byte("build:\n  script: 5\n"))
	if err == nil || !strings.Contains(err.Error(), ".gitlab-ci.yml") {
		t.Fatalf("got %v", err)
	}
}

func TestNoSchema(t *testing.T) {
	err := File("drone", "x.yml", []byte("a: 1\n"))
	if !errors.Is(err, ErrNoSchema) || !strings.Contains(err.Error(), "drone") {
		t.Fatalf("got %v", err)
	}
}

func TestBadYAML(t *testing.T) {
	if err := File(provider.GHA, "x.yml", []byte("a: [\n")); err == nil {
		t.Fatal("want error")
	}
}
