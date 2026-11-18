package provider

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

type fake struct {
	id     string
	detect string
}

func (f fake) ID() string { return f.id }
func (f fake) Detect(fsys fs.FS) bool {
	_, err := fs.Stat(fsys, f.detect)
	return err == nil
}
func (f fake) Parse(fs.FS) (*ir.Project, *report.Report, error) { return nil, nil, nil }
func (f fake) Emit(*ir.Project) (map[string][]byte, *report.Report, error) {
	return nil, nil, nil
}
func (f fake) Caps() caps.Matrix { return nil }

func TestRegisterDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.Register(fake{id: GHA})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r.Register(fake{id: GHA})
}

func TestGetUnknown(t *testing.T) {
	r := NewRegistry()
	r.Register(fake{id: GHA})
	r.Register(fake{id: GitLab})
	_, err := r.Get("nope")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"nope", GHA, GitLab} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	p, err := r.Get(GHA)
	if err != nil || p.ID() != GHA {
		t.Fatalf("Get(gha) = %v, %v", p, err)
	}
}

func TestDetectSorted(t *testing.T) {
	r := NewRegistry()
	r.Register(fake{id: GitLab, detect: ".gitlab-ci.yml"})
	r.Register(fake{id: GHA, detect: ".github/workflows"})
	r.Register(fake{id: Drone, detect: ".drone.yml"})
	fsys := fstest.MapFS{
		".gitlab-ci.yml":           {Data: []byte("x")},
		".github/workflows/ci.yml": {Data: []byte("x")},
	}
	got := r.Detect(fsys)
	want := []string{GHA, GitLab}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Detect = %v, want %v", got, want)
	}
	if ids := r.IDs(); strings.Join(ids, ",") != strings.Join([]string{Drone, GHA, GitLab}, ",") {
		t.Fatalf("IDs = %v", ids)
	}
}
