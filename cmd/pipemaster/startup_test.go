package main

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/validate/schemas"
)

// The compressed schema sets must stay compressed until a validation needs them.
func TestStartupLeavesSchemasCompressed(t *testing.T) {
	if schemas.Loaded() {
		t.Fatal("a compressed schema was decompressed during program start")
	}
}
