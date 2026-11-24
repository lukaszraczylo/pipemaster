//go:build behave

package behave

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	kubeContext = "kiri"
	kiriDir     = ".kiri"
)

// Env holds the paths and the safe process environment of one harness run.
type Env struct {
	Root       string
	Tools      string
	PM         string
	Work       string
	DockerHost string
	DockerCfg  string
	Kubeconfig string
	ports      atomic.Int64
	bakeMu     sync.Mutex
}

// NewEnv resolves the repository root, builds the pipemaster binary and prepares the docker config.
func NewEnv(ctx context.Context, work string) (*Env, error) {
	root, err := findRoot()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	e := &Env{
		Root:       root,
		Tools:      filepath.Join(root, ".tools"),
		Work:       work,
		PM:         filepath.Join(work, "pipemaster"),
		DockerHost: "unix://" + filepath.Join(home, kiriDir, "run", "docker.sock"),
		DockerCfg:  filepath.Join(work, "dockercfg"),
		Kubeconfig: filepath.Join(home, kiriDir, "kubeconfig"),
	}
	if err := os.MkdirAll(e.DockerCfg, 0o750); err != nil { // #nosec G703 -- a directory under the run work dir
		return nil, fmt.Errorf("docker config dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(e.DockerCfg, "config.json"), []byte("{}"), 0o600); err != nil { // #nosec G703 -- a path under the run work dir
		return nil, fmt.Errorf("docker config: %w", err)
	}
	out, _, err := e.run(ctx, runOpts{Dir: root, Timeout: 5 * time.Minute}, "go", "build", "-o", e.PM, "./cmd/pipemaster")
	if err != nil {
		return nil, fmt.Errorf("build pipemaster: %w\n%s", err, out)
	}
	return e, nil
}

func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working dir: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found above the working directory")
		}
		dir = parent
	}
}

// Tool returns the path of a pinned tool under .tools/bin.
func (e *Env) Tool(name string) string { return filepath.Join(e.Tools, "bin", name) }

// GitlabCILocal returns the pinned gitlab-ci-local binary.
func (e *Env) GitlabCILocal() string {
	return filepath.Join(e.Tools, "npm", "node_modules", ".bin", "gitlab-ci-local")
}

// Require fails when a pinned tool is missing, naming the make target that installs it.
func (e *Env) Require(paths ...string) error {
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("%s is missing: run make behave-tools", p)
		}
	}
	return nil
}

// baseEnv is the process environment of every engine call: Kiri docker, no host kube config.
func (e *Env) baseEnv() []string {
	keep := []string{"PATH", "HOME", "TMPDIR", "LANG", "TERM", "USER"}
	var env []string
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env,
		"DOCKER_HOST="+e.DockerHost,
		"DOCKER_CONFIG="+e.DockerCfg,
		"KUBECONFIG="+e.Kubeconfig,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
}

type runOpts struct {
	Dir     string
	Stdin   string
	Env     []string
	Timeout time.Duration
}

// run executes a command and returns its combined output and exit status. A non-zero exit is no error. A timeout
// or a start failure is.
func (e *Env) run(ctx context.Context, o runOpts, name string, args ...string) (string, int, error) {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 G702 -- the harness runs pinned tools with fixed arguments
	cmd.Dir = o.Dir
	cmd.Env = append(e.baseEnv(), o.Env...)
	if o.Stdin != "" {
		cmd.Stdin = strings.NewReader(o.Stdin)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return buf.String(), 0, nil
	case ctx.Err() != nil:
		return buf.String(), -1, fmt.Errorf("%s timed out after %s", name, o.Timeout)
	case errors.As(err, &ee):
		return buf.String(), ee.ExitCode(), nil
	}
	return buf.String(), -1, fmt.Errorf("run %s: %w", name, err)
}

// Docker runs the docker CLI against Kiri.
func (e *Env) Docker(ctx context.Context, timeout time.Duration, stdin string, args ...string) (string, int, error) {
	return e.run(ctx, runOpts{Timeout: timeout, Stdin: stdin}, "docker", args...)
}

// CheckDocker fails unless the Kiri docker daemon answers.
func (e *Env) CheckDocker(ctx context.Context) error {
	out, code, err := e.Docker(ctx, time.Minute, "", "version", "--format", "{{.Server.Version}}")
	if err != nil || code != 0 {
		return fmt.Errorf("the Kiri docker daemon does not answer on %s (start Kiri): %v %s", e.DockerHost, err, strings.TrimSpace(out))
	}
	return nil
}

// Kubectl runs kubectl against the Kiri context only.
func (e *Env) Kubectl(ctx context.Context, timeout time.Duration, stdin string, args ...string) (string, int, error) {
	full := append([]string{"--context", kubeContext}, args...)
	return e.run(ctx, runOpts{Timeout: timeout, Stdin: stdin}, "kubectl", full...)
}

// RequireKiriContext refuses to continue unless the Kiri kubeconfig names the kiri context as current.
func (e *Env) RequireKiriContext(ctx context.Context) error {
	if _, err := os.Stat(e.Kubeconfig); err != nil { // #nosec G703 -- the fixed Kiri kubeconfig path
		return fmt.Errorf("kube config %s is missing: %w", e.Kubeconfig, err)
	}
	out, code, err := e.run(ctx, runOpts{Timeout: time.Minute}, "kubectl", "config", "current-context")
	if err != nil || code != 0 {
		return fmt.Errorf("kubectl config current-context failed: %v %s", err, strings.TrimSpace(out))
	}
	if got := strings.TrimSpace(out); got != kubeContext {
		return fmt.Errorf("active kube context is %q, not %q: refusing to run", got, kubeContext)
	}
	return nil
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }
