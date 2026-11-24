package actions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const hostileRef = "--upload-pack=touch PWNED"

// checkoutScript returns the emulated checkout script of a target.
func checkoutScript(t *testing.T, with map[string]string) string {
	t.Helper()
	ref := ir.ActionRef{Kind: ir.ActionRepo, Host: "https://github.com", Owner: "actions", Repo: "checkout", Ref: "v7"}
	got, ok := Lookup(ref, "drone", with)
	if !ok || got.Kind != KindScript || len(got.Script) != 1 {
		t.Fatalf("no script: %+v", got)
	}
	return got.Script[0]
}

// repoSetup makes a repository whose origin is a bare copy, so a fetch has a remote.
const repoSetup = `set -e
git config --global user.email t@t; git config --global user.name t; git config --global init.defaultBranch main
git init -q origin.git --bare; git init -q work; cd work
echo a > f; git add f; git commit -qm a; git remote add origin ../origin.git; git push -q origin main
`

func runScript(t *testing.T, ref, script string, docker bool) (string, error) {
	t.Helper()
	body := repoSetup + "export PIPEMASTER_CHECKOUT_REF='" + ref + "'\n" + script
	if !docker {
		cmd := exec.Command("sh", "-c", body) //nolint:gosec // fixed shell, script from the embedded registry
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "GIT_CONFIG_NOSYSTEM=1")
		cmd.Dir = t.TempDir()
		out, err := cmd.CombinedOutput()
		if _, serr := os.Stat(filepath.Join(cmd.Dir, "work", "PWNED")); serr == nil {
			t.Errorf("the hostile ref created a file")
		}
		return string(out), err
	}
	image := loaded.entries[0].emulate.Image
	cmd := exec.Command("docker", "run", "--rm", "--entrypoint", "sh", image, "-c", body+"\ntest ! -e /PWNED && test ! -e PWNED") //nolint:gosec // fixed docker args, image from the embedded registry
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestHostileRefIsNotAGitOption(t *testing.T) {
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	script := checkoutScript(t, map[string]string{"ref": hostileRef})
	if !strings.Contains(script, "fetch -- origin") && !strings.Contains(script, "--depth=\"$depth\" -- origin") {
		t.Fatalf("fetch has no -- before the ref")
	}
	out, err := runScript(t, hostileRef, script, false)
	if err == nil || !strings.Contains(out, "starts with -") {
		t.Fatalf("hostile ref was not rejected: %v\n%s", err, out)
	}
}

func TestHostileRefInThePinnedImage(t *testing.T) {
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("set DOCKER_HOST to run the pinned image")
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	script := checkoutScript(t, map[string]string{"ref": hostileRef})
	out, err := runScript(t, hostileRef, script, true)
	if err == nil || !strings.Contains(out, "starts with -") {
		t.Fatalf("hostile ref was not rejected in the image: %v\n%s", err, out)
	}
}

func TestPlainRefStillChecksOut(t *testing.T) {
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	script := checkoutScript(t, map[string]string{"ref": "main"})
	if out, err := runScript(t, "main", script, false); err != nil {
		t.Fatalf("plain ref failed: %v\n%s", err, out)
	}
}

func TestPlainRefChecksOutInThePinnedImage(t *testing.T) {
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("set DOCKER_HOST to run the pinned image")
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if out, err := runScript(t, "main", checkoutScript(t, map[string]string{"ref": "main"}), true); err != nil {
		t.Fatalf("plain ref failed in the image: %v\n%s", err, out)
	}
}
