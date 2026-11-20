package woodpecker_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/validate"
)

func TestSchemaIsRegistered(t *testing.T) {
	if err := validate.File("woodpecker", ".woodpecker.yaml", []byte(minimal)); err != nil {
		t.Fatalf("minimal workflow does not validate: %v", err)
	}
	if err := validate.File("woodpecker", ".woodpecker.yaml", []byte("pipeline: []\n")); err == nil {
		t.Fatal("a removed 2.x key must fail the schema")
	}
	got := validate.Patterns("woodpecker")
	if len(got) == 0 {
		t.Fatal("no file patterns registered")
	}
}
