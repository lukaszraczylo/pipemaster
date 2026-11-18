package roundtrip_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/cli"
	"github.com/lukaszraczylo/pipemaster/meta"
)

const exitStale = 4

func runCLI(args ...string) (int, string) {
	var out, errb bytes.Buffer
	code := cli.Run(args, &out, &errb)
	return code, errb.String()
}

func TestStaleRefuses(t *testing.T) {
	src := filepath.Join(featuresRoot, "gha", "features", "job.env")
	gl := t.TempDir()
	if code, msg := runCLI("convert", src, "--to", "gitlab", "-o", gl); code != 0 {
		t.Fatalf("convert to gitlab = %d\n%s", code, msg)
	}
	file := filepath.Join(gl, ".gitlab-ci.yml")
	data, err := os.ReadFile(file) // #nosec G304 -- temp dir path
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(data, "\n# edited by hand\n"...), 0o600); err != nil { // #nosec G703 -- path under t.TempDir
		t.Fatal(err)
	}

	m, err := meta.Load(os.DirFS(gl))
	if err != nil || m == nil {
		t.Fatalf("load meta: %v %v", m, err)
	}
	stale, err := m.Stale(os.DirFS(gl))
	if err == nil || len(stale) != 1 || stale[0] != ".gitlab-ci.yml" {
		t.Fatalf("Stale = %v, %v; want [.gitlab-ci.yml] and an error", stale, err)
	}

	back := t.TempDir()
	code, msg := runCLI("convert", gl, "--to", "gha", "-o", back)
	if code != exitStale || !strings.Contains(msg, ".gitlab-ci.yml") {
		t.Fatalf("convert without --force = %d, want %d naming the file\n%s", code, exitStale, msg)
	}
	if code, msg := runCLI("convert", gl, "--to", "gha", "-o", back, "--force"); code != 0 {
		t.Fatalf("convert --force = %d\n%s", code, msg)
	}
}
