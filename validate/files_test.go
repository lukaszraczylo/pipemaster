package validate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider"
	_ "github.com/lukaszraczylo/pipemaster/provider/gha"
	_ "github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/validate"
)

func TestValidGHA(t *testing.T) {
	y := "name: ci\non: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"
	if err := validate.File(provider.GHA, "ci.yml", []byte(y)); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidGHA(t *testing.T) {
	y := "on: push\njobs:\n  build:\n    steps:\n      - run: echo hi\n"
	err := validate.File(provider.GHA, "ci.yml", []byte(y))
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "runs-on") || !strings.Contains(err.Error(), "ci.yml") {
		t.Fatalf("error lacks runs-on or file name: %v", err)
	}
}

func TestValidGitLab(t *testing.T) {
	y := "build:\n  script:\n    - echo hi\n"
	if err := validate.File(provider.GitLab, ".gitlab-ci.yml", []byte(y)); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidGitLab(t *testing.T) {
	err := validate.File(provider.GitLab, ".gitlab-ci.yml", []byte("build:\n  script: 5\n"))
	if err == nil || !strings.Contains(err.Error(), ".gitlab-ci.yml") || !strings.Contains(err.Error(), "script") {
		t.Fatalf("got %v", err)
	}
}

func TestNoSchema(t *testing.T) {
	err := validate.File("drone", "x.yml", []byte("a: 1\n"))
	if !errors.Is(err, validate.ErrNoSchema) || !strings.Contains(err.Error(), "drone") {
		t.Fatalf("got %v", err)
	}
}

func TestBadYAML(t *testing.T) {
	if err := validate.File(provider.GHA, "x.yml", []byte("a: [\n")); err == nil {
		t.Fatal("want error")
	}
}

func TestPatterns(t *testing.T) {
	if got := validate.Patterns(provider.GitLab); len(got) != 1 || got[0] != ".gitlab-ci.yml" {
		t.Fatalf("gitlab = %v", got)
	}
	if got := validate.Patterns(provider.GHA); len(got) != 2 {
		t.Fatalf("gha = %v", got)
	}
	if got := validate.Patterns(provider.Drone); got != nil {
		t.Fatalf("drone = %v", got)
	}
}

func TestEveryYAMLDocumentIsValidated(t *testing.T) {
	header := "spec:\n  inputs:\n    t:\n      default: x\n---\n"
	if err := validate.File(provider.GitLab, ".gitlab-ci.yml", []byte(header+"build:\n  script:\n    - echo hi\n")); err != nil {
		t.Fatalf("valid header and body: %v", err)
	}
	err := validate.File(provider.GitLab, ".gitlab-ci.yml", []byte(header+"build:\n  script: 5\n"))
	if err == nil || !strings.Contains(err.Error(), "script") {
		t.Fatalf("an invalid body after a spec header must be rejected, got %v", err)
	}
}
