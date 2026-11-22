package argo_test

import (
	"strings"
	"testing"
)

func TestEditedCacheMountDoesNotRestoreTheCache(t *testing.T) {
	pl, rep := editedMarked(t, "mountPath: /workspace/.cache", "mountPath: /workspace/.other")
	if c := jobOf(pl, "build").Cache; len(c) > 0 {
		t.Errorf("cache %+v came back although its mount was edited", c)
	}
	wantIgnored(t, rep)
}

func TestRemovedCacheClaimDoesNotRestoreTheCache(t *testing.T) {
	files, _ := emit(t, markedProject())
	src := files[ciFile]
	i := strings.Index(src, "  volumeClaimTemplates:")
	j := strings.Index(src, "  templates:")
	files[ciFile] = src[:i] + src[j:]
	p, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if c := jobOf(p.Pipelines[0], "build").Cache; len(c) > 0 {
		t.Errorf("cache %+v came back although its claim was removed", c)
	}
	wantIgnored(t, rep)
}
