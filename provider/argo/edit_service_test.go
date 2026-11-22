package argo_test

import (
	"testing"
)

func TestEditedServiceImageDoesNotGetTheMarkedEntrypoint(t *testing.T) {
	pl, rep := editedMarked(t, "image: postgres:17", "image: postgres:16")
	sv := jobOf(pl, "build").Services[0]
	if len(sv.Image.Entrypoint) > 0 {
		t.Errorf("entrypoint %v came back for an edited image %s", sv.Image.Entrypoint, sv.Image.Ref)
	}
	wantIgnored(t, rep)
}
