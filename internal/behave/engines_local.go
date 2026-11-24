//go:build behave

package behave

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	repoFullName = "pipemaster/behave"
	prRefPath    = "refs/pull/1/head"
)

func newTrace(out string, code int, lines []string, d time.Duration) *Trace {
	return &Trace{Markers: markers(lines), Log: out, Exit: code, Duration: d}
}

// ---- act (GitHub Actions)

type actEngine struct{ env *Env }

const actPortBase = 34600

var actLine = regexp.MustCompile(`^\[[^\]]+\]\s+\|\s?(.*)$`)

func (a *actEngine) payload(w *Workspace) (string, []byte, error) {
	name, body := githubPayload(w)
	raw, err := json.Marshal(body)
	return name, raw, err
}

func (a *actEngine) Run(ctx context.Context, w *Workspace) (*Trace, error) {
	if err := a.env.Require(a.env.Tool("act")); err != nil {
		return nil, err
	}
	name, raw, err := a.payload(w)
	if err != nil {
		return nil, err
	}
	evFile := filepath.Join(filepath.Dir(w.Dir), filepath.Base(w.Dir)+"-event.json")
	if err := os.WriteFile(evFile, raw, 0o600); err != nil {
		return nil, err
	}
	art := filepath.Join(filepath.Dir(w.Dir), filepath.Base(w.Dir)+"-artifacts")
	args := []string{name, "-e", evFile, "--container-architecture", "linux/arm64", "--concurrent-jobs", "1", "--artifact-server-path", art, "--artifact-server-port", strconv.FormatInt(actPortBase+a.env.ports.Add(1), 10)}
	for _, label := range []string{"ubuntu-latest", "ubuntu-24.04", "ubuntu-22.04", "ubuntu-20.04"} {
		args = append(args, "-P", label+"=node:20-bookworm-slim")
	}
	start := time.Now()
	out, code, err := a.env.run(ctx, runOpts{Dir: w.Dir, Timeout: timeoutLocal, Env: []string{"ACT_DISABLE_VERSION_CHECK=1"}}, a.env.Tool("act"), args...)
	if err != nil {
		return nil, err
	}
	return newTrace(out, code, payloadLines(out, actLine), time.Since(start)), nil
}

// ---- Woodpecker

type woodpeckerEngine struct{ env *Env }

var wpLine = regexp.MustCompile(`^\[[^\]]+:L\d+:\d+s\] (.*)$`)

func (e *woodpeckerEngine) Run(ctx context.Context, w *Workspace) (*Trace, error) {
	bin := e.env.Tool("woodpecker-cli")
	if err := e.env.Require(bin); err != nil {
		return nil, err
	}
	target := ".woodpecker"
	switch {
	case exists(w.Dir, ".woodpecker"):
	case exists(w.Dir, ".woodpecker.yaml"):
		target = ".woodpecker.yaml"
	case exists(w.Dir, ".woodpecker.yml"):
		target = ".woodpecker.yml"
	default:
		return nil, blocked("no Woodpecker file in the converted output")
	}
	branch, ref := w.Ev.Branch, w.Ev.Ref()
	event := "push"
	args := []string{"exec", "--local", "--repo-path", w.Dir, "--backend-engine", "docker", "--repo-default-branch", mainBranch, "--commit-sha", w.SHA}
	switch w.Ev.Kind {
	case kindTag:
		event, branch = "tag", ""
	case kindPR:
		event, branch, ref = "pull_request", w.Ev.Target, prRefPath
		args = append(args, "--commit-refspec", w.Ev.Branch+":"+w.Ev.Target)
	}
	args = append(args, "--pipeline-event", event, "--commit-branch", branch, "--commit-ref", ref, target)
	start := time.Now()
	out, code, err := e.env.run(ctx, runOpts{Dir: w.Dir, Timeout: timeoutLocal}, bin, args...)
	if err != nil {
		return nil, err
	}
	return newTrace(out, code, payloadLines(out, wpLine), time.Since(start)), nil
}

// ---- Drone

type droneEngine struct{ env *Env }

var droneLine = regexp.MustCompile(`^\[[^\]:]+:\d+\] (.*)$`)

func dronePipelines(path string) ([]string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the workspace file of the harness
	if err != nil {
		return nil, err
	}
	var names []string
	for _, doc := range strings.Split(string(raw), "\n---") {
		var d struct {
			Kind string `yaml:"kind"`
			Name string `yaml:"name"`
		}
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if d.Kind == "pipeline" {
			names = append(names, d.Name)
		}
	}
	return names, nil
}

func (e *droneEngine) Run(ctx context.Context, w *Workspace) (*Trace, error) {
	bin := e.env.Tool("drone")
	if err := e.env.Require(bin); err != nil {
		return nil, err
	}
	file := filepath.Join(w.Dir, ".drone.yml")
	names, err := dronePipelines(file)
	if err != nil {
		return nil, blocked("no usable .drone.yml in the converted output: %v", err)
	}
	branch, event := w.Ev.Branch, "push"
	switch w.Ev.Kind {
	case kindTag:
		event, branch = "tag", ""
	case kindPR:
		event, branch = "pull_request", w.Ev.Target
	}
	envFile := filepath.Join(filepath.Dir(w.Dir), filepath.Base(w.Dir)+".env")
	vars := fmt.Sprintf("DRONE_COMMIT_SHA=%s\nDRONE_COMMIT=%s\nDRONE_COMMIT_REF=%s\nDRONE_COMMIT_BRANCH=%s\nDRONE_TAG=%s\n", w.SHA, w.SHA, w.Ev.Ref(), branch, w.Ev.Tag)
	if err := os.WriteFile(envFile, []byte(vars), 0o600); err != nil {
		return nil, err
	}
	tr := &Trace{}
	start := time.Now()
	for _, n := range names {
		args := []string{"exec", "--pipeline", n, "--event", event, "--branch=" + branch, "--env-file", envFile, ".drone.yml"}
		out, code, err := e.env.run(ctx, runOpts{Dir: w.Dir, Timeout: timeoutLocal}, bin, args...)
		if err != nil {
			return nil, err
		}
		tr.Log += out
		tr.Markers = append(tr.Markers, markers(payloadLines(out, droneLine))...)
		if code != 0 {
			tr.Exit = code
			break
		}
	}
	tr.Duration = time.Since(start)
	return tr, nil
}

// ---- GitLab (gitlab-ci-local)

type gitlabEngine struct{ env *Env }

var glLine = regexp.MustCompile(`^\S+\s+> (.*)$`)

// kiriShim works around two Kiri docker quirks: "docker cp ctr:/dir/. dest" copies the directory itself, and a
// copy into a container needs the target parent to exist (the harness bakes it into the images).
const kiriShim = `#!/bin/bash
D="${PM_DOCKER:-docker}"
[ -n "$PM_SHIM_LOG" ] && echo "$@" >> "$PM_SHIM_LOG"
if [ "$1" = cp ] && [[ "$2" == *:*/. ]] && [[ "$2" != /* ]]; then
  src=${2%/.}; tmp=$(mktemp -d); mkdir -p "$3"
  "$D" cp "$src" "$tmp" || { rc=$?; rm -rf "$tmp"; exit $rc; }
  cp -a "$tmp/$(basename "${src#*:}")/." "$3"; rc=$?; rm -rf "$tmp"; exit $rc
fi
if [ "$1" = cp ] && [[ "$2" != *:* ]] && [[ "$2" == */. ]] && [[ "$3" == *:* ]]; then
  for e in "${2%/.}"/* "${2%/.}"/.[!.]*; do [ -e "$e" ] && [ "$(basename "$e")" != .git ] || continue; "$D" cp "$e" "$3/" || exit $?; done; exit 0
fi
exec "$D" "$@"
`

const defaultImage = "alpine:3.20"

const helperImage = "docker.io/firecow/gitlab-ci-local-util:latest"

const bakedDir = "/builds/fallback.group/fallback.project"

var imageSafe = regexp.MustCompile(`[^a-z0-9._-]`)

func (e *gitlabEngine) bake(ctx context.Context, image string) (string, error) {
	e.env.bakeMu.Lock()
	defer e.env.bakeMu.Unlock()
	tag := "pm-behave/" + imageSafe.ReplaceAllString(strings.ToLower(image), "_") + ":baked"
	if _, code, err := e.env.Docker(ctx, time.Minute, "", "image", "inspect", tag); err == nil && code == 0 {
		return tag, nil
	}
	// "docker build" fails on Kiri ("layer is gone"), so the baked image comes from run and commit.
	ctr := "pm-bake-" + strconv.FormatInt(e.env.ports.Add(1), 10)
	out, code, err := e.env.Docker(ctx, 5*time.Minute, "", "run", "--name", ctr, "--entrypoint", "sh", image, "-c", "mkdir -p "+bakedDir)
	if err == nil && code == 0 {
		out, code, err = e.env.Docker(ctx, time.Minute, "", "commit", "--change", "ENTRYPOINT []", "--change", "CMD [\"sh\"]", ctr, tag)
	}
	_, _, _ = e.env.Docker(ctx, time.Minute, "", "rm", "-f", ctr) // only the container this function named
	if err != nil || code != 0 {
		return "", fmt.Errorf("bake %s: %v %s", image, err, out)
	}
	return tag, nil
}

// rewriteImages points every image of the file at its baked twin and returns the rewritten document.
func (e *gitlabEngine) rewriteImages(ctx context.Context, raw []byte) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	var walkErr error
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		for i := 0; i < len(n.Content); i++ {
			c := n.Content[i]
			if n.Kind == yaml.MappingNode && i%2 == 0 && c.Value == "image" && i+1 < len(n.Content) {
				v := n.Content[i+1]
				target := v
				if v.Kind == yaml.MappingNode {
					target = nil
					for j := 0; j+1 < len(v.Content); j += 2 {
						if v.Content[j].Value == "name" {
							target = v.Content[j+1]
						}
					}
				}
				if target != nil && target.Kind == yaml.ScalarNode && !strings.Contains(target.Value, "$") && walkErr == nil {
					baked, err := e.bake(ctx, target.Value)
					if err != nil {
						walkErr = err
					} else {
						target.Value = baked
					}
				}
				continue
			}
			walk(c)
		}
	}
	walk(&root)
	if walkErr != nil {
		return nil, walkErr
	}
	if err := e.defaultImageNode(ctx, &root); err != nil {
		return nil, err
	}
	return yaml.Marshal(&root)
}

// defaultImageNode gives a file without any default image the baked alpine, because gitlab-ci-local runs imageless jobs on the host.
func (e *gitlabEngine) defaultImageNode(ctx context.Context, root *yaml.Node) error {
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	top := root.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if k := top.Content[i].Value; k == "default" || k == "image" {
			return nil
		}
	}
	baked, err := e.bake(ctx, defaultImage)
	if err != nil {
		return err
	}
	str := func(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }
	img := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{str("image"), str(baked)}}
	top.Content = append(top.Content, str("default"), img)
	return nil
}

func (e *gitlabEngine) variables(w *Workspace) []string {
	v := map[string]string{
		"CI_PIPELINE_SOURCE": "push",
		"CI_COMMIT_BRANCH":   w.Ev.Branch,
		"CI_COMMIT_TAG":      w.Ev.Tag,
		"CI_COMMIT_REF_NAME": w.Ev.Branch,
		"CI_COMMIT_SHA":      w.SHA,
	}
	switch w.Ev.Kind {
	case kindTag:
		v["CI_COMMIT_REF_NAME"] = w.Ev.Tag
	case kindPR:
		v["CI_PIPELINE_SOURCE"] = "merge_request_event"
		v["CI_COMMIT_BRANCH"] = ""
		v["CI_MERGE_REQUEST_IID"] = "1"
		v["CI_MERGE_REQUEST_SOURCE_BRANCH_NAME"] = w.Ev.Branch
		v["CI_MERGE_REQUEST_TARGET_BRANCH_NAME"] = w.Ev.Target
	}
	var out, ignore []string
	for k, val := range v {
		out = append(out, "--variable", k+"="+val)
		ignore = append(ignore, k)
	}
	return append(out, "--ignore-predefined-vars", strings.Join(ignore, ","))
}

func (e *gitlabEngine) Run(ctx context.Context, w *Workspace) (*Trace, error) {
	bin := e.env.GitlabCILocal()
	if err := e.env.Require(bin); err != nil {
		return nil, err
	}
	file := filepath.Join(w.Dir, ".gitlab-ci.yml")
	raw, err := os.ReadFile(file) // #nosec G304 -- the workspace file of the harness
	if err != nil {
		return nil, blocked("no .gitlab-ci.yml in the converted output")
	}
	rewritten, err := e.rewriteImages(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("prepare images: %w", err)
	}
	if err := os.WriteFile(file, rewritten, 0o600); err != nil { // #nosec G703 -- a file of the harness workspace
		return nil, err
	}
	shim := filepath.Join(e.env.Work, "docker-kiri-shim")
	if err := os.WriteFile(shim, []byte(kiriShim), 0o700); err != nil { // #nosec G306 -- an executable helper script
		return nil, err
	}
	helper, err := e.bake(ctx, helperImage)
	if err != nil {
		return nil, fmt.Errorf("prepare helper image: %w", err)
	}
	args := []string{"--helper-image", helper, "--container-executable", shim, "--pull-policy", "if-not-present", "--artifacts-to-source=false"}
	args = append(args, e.variables(w)...)
	start := time.Now()
	out, code, err := e.env.run(ctx, runOpts{Dir: w.Dir, Timeout: timeoutLocal, Env: []string{"PM_SHIM_LOG=" + w.Dir + ".shim.log"}}, bin, args...)
	if err != nil {
		return nil, err
	}
	return newTrace(out, code, payloadLines(out, glLine), time.Since(start)), nil
}
