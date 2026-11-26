package roundtrip_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	droneName    = ".drone.yml"
	gitlabName   = ".gitlab-ci.yml"
	droneNodeDoc = "kind: pipeline\ntype: docker\nname: default\nnode:\n%s\nsteps:\n  - name: s\n    image: alpine:3\n    commands:\n      - x\n"
)

// convertDir converts the files of src from one dialect to another and returns the output directory.
func convertDir(t *testing.T, src, from, to string) string {
	t.Helper()
	out := t.TempDir()
	if code, _, msg := runOut("convert", src, "--from", from, "--to", to, "-o", out); code != 0 {
		t.Fatalf("convert %s->%s = %d\n%s", from, to, code, msg)
	}
	return out
}

func writeSource(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// wantExactTrip converts the source to mid and back, and requires the file to equal the same-dialect conversion.
func wantExactTrip(t *testing.T, name, dialect, mid, content string) {
	t.Helper()
	src := writeSource(t, name, content)
	want := string(outputFiles(t, convertDir(t, src, dialect, dialect))[name])
	back := string(outputFiles(t, convertDir(t, convertDir(t, src, dialect, mid), mid, dialect))[name])
	if back != want {
		t.Errorf("%s -> %s -> %s changed the file\nwant:\n%s\ngot:\n%s", dialect, mid, dialect, want, back)
	}
}

func TestRunnerLabelsRoundTripExactly(t *testing.T) {
	gitlab := "build:\n  tags: [docker, linux]\n  script: [echo hi]\n"
	nodes := map[string]string{
		"selector": "  region: eu",
		"mixed":    "  gpu: \"true\"\n  region: eu\n  zone: a",
		"tags":     "  docker: \"true\"\n  linux: \"true\"",
	}
	for _, mid := range []string{"drone", "woodpecker", "tekton", "argo"} {
		t.Run("gitlab-tags-via-"+mid, func(t *testing.T) { wantExactTrip(t, gitlabName, "gitlab", mid, gitlab) })
		if mid == "drone" {
			continue
		}
		for name, node := range nodes {
			t.Run("drone-"+name+"-via-"+mid, func(t *testing.T) {
				wantExactTrip(t, droneName, "drone", mid, strings.Replace(droneNodeDoc, "%s", node, 1))
			})
		}
	}
	for name, node := range nodes {
		t.Run("drone-"+name+"-via-gitlab", func(t *testing.T) {
			wantExactTrip(t, droneName, "drone", "gitlab", strings.Replace(droneNodeDoc, "%s", node, 1))
		})
	}
}

func TestDroneNodeKeepsBothKindsOnWoodpecker(t *testing.T) {
	src := writeSource(t, droneName, strings.Replace(droneNodeDoc, "%s", "  gpu: \"true\"\n  region: eu", 1))
	var all strings.Builder
	for _, b := range outputFiles(t, convertDir(t, src, "drone", "woodpecker")) {
		all.Write(b)
	}
	out := all.String()
	if !strings.Contains(out, "gpu: \"true\"") || !strings.Contains(out, "region: eu") {
		t.Errorf("both the tag and the selector entry must reach labels:\n%s", out)
	}
}
