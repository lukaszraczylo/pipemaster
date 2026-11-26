//go:build behave

package behave

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	gitSrvName     = "gitsrv"
	gitSrvPort     = 9418
	gitSrvImage    = "alpine/git:v2.54.1"
	gitSrvWait     = "180s"
	catalogGitURL  = cloneHostURL + "tektoncd-catalog/"
	repoArchive    = "repo.tgz"
	repoArchiveDir = "/seed"
)

// clonesGitHub is true when a converted Tekton or Argo document clones a repository from GitHub. The clone of the
// pipeline repository names github.com in the converted file, and the Kiri cluster cannot serve that repository from there.
func clonesGitHub(docs []map[string]any) bool {
	found := false
	for _, d := range docs {
		walkStrings(d, func(s string) string {
			found = found || isRepoCloneURL(s)
			return s
		})
	}
	return found
}

// isRepoCloneURL is true for a GitHub URL other than the Tekton catalog, which the git resolver fetches over the network.
func isRepoCloneURL(s string) bool {
	return strings.HasPrefix(s, cloneHostURL) && !strings.HasPrefix(s, catalogGitURL)
}

// walkStrings replaces every string value in a decoded document by the result of fn.
func walkStrings(v any, fn func(string) string) any {
	switch t := v.(type) {
	case string:
		return fn(t)
	case map[string]any:
		for k, e := range t {
			t[k] = walkStrings(e, fn)
		}
	case []any:
		for i, e := range t {
			t[i] = walkStrings(e, fn)
		}
	}
	return v
}

// rewriteClone points the clone URLs of the pipeline repository at the in-cluster git server.
func rewriteClone(docs []map[string]any, base string) {
	for _, d := range docs {
		walkStrings(d, func(s string) string {
			if isRepoCloneURL(s) {
				return base + strings.TrimPrefix(s, cloneHostURL)
			}
			return s
		})
	}
}

// gitSrvBase is the git:// base URL of the in-cluster server of a namespace.
func gitSrvBase(ns string) string {
	return fmt.Sprintf("git://%s.%s.svc.cluster.local/", gitSrvName, ns)
}

// useRepoHost serves the workspace repository inside the namespace and rewrites the clone URLs of the converted
// documents to it, when they clone from GitHub. The documents stay as converted otherwise.
func (k *Kube) useRepoHost(ctx context.Context, ns string, docs []map[string]any, w *Workspace) error {
	if !clonesGitHub(docs) {
		return nil
	}
	if err := k.serveRepo(ctx, ns, w); err != nil {
		return fmt.Errorf("serve the repository in the cluster: %w", err)
	}
	rewriteClone(docs, gitSrvBase(ns))
	return nil
}

// serveRepo starts a git daemon pod in the namespace that serves a bare copy of the workspace as <repoFullName>.git.
func (k *Kube) serveRepo(ctx context.Context, ns string, w *Workspace) error {
	tmp, err := os.MkdirTemp("", "behave-repo-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // a temp dir that this call made; a leftover is no failure
	bare := filepath.Join(tmp, filepath.FromSlash(repoFullName)+".git")
	if out, code, err := k.env.run(ctx, runOpts{Timeout: kubeShort}, "git", "clone", "-q", "--bare", w.Dir, bare); err != nil || code != 0 {
		return fmt.Errorf("bare clone: %v %s", err, tail(out, 300))
	}
	archive, err := tarGz(tmp)
	if err != nil {
		return err
	}
	cm := map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": gitSrvName},
		"binaryData": map[string]any{repoArchive: base64.StdEncoding.EncodeToString(archive)},
	}
	script := fmt.Sprintf("apk add --no-cache git-daemon >/dev/null && mkdir -p /srv/git && tar xzf %s/%s -C /srv/git && exec git -c uploadpack.allowAnySHA1InWant=true daemon --reuseaddr --export-all --base-path=/srv/git --listen=0.0.0.0 --port=%d",
		repoArchiveDir, repoArchive, gitSrvPort)
	pod := map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": gitSrvName, "labels": map[string]any{"app": gitSrvName}},
		"spec": map[string]any{
			"automountServiceAccountToken": false,
			"containers": []any{map[string]any{
				"name": "git", "image": mirrorRef(gitSrvImage), "command": []any{"sh", "-c", script},
				"ports":          []any{map[string]any{"containerPort": gitSrvPort}},
				"readinessProbe": map[string]any{"tcpSocket": map[string]any{"port": gitSrvPort}},
				"volumeMounts":   []any{map[string]any{"name": "seed", "mountPath": repoArchiveDir}},
			}},
			"volumes": []any{map[string]any{"name": "seed", "configMap": map[string]any{"name": gitSrvName}}},
		},
	}
	svc := map[string]any{
		"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": gitSrvName},
		"spec": map[string]any{"selector": map[string]any{"app": gitSrvName}, "ports": []any{map[string]any{"port": gitSrvPort}}},
	}
	var doc bytes.Buffer
	for _, o := range []map[string]any{cm, pod, svc} {
		raw, err := yaml.Marshal(o)
		if err != nil {
			return err
		}
		doc.WriteString("---\n")
		doc.Write(raw)
	}
	if err := k.applyRetry(ctx, ns, doc.String()); err != nil {
		return err
	}
	out, code, err := k.env.Kubectl(ctx, timeoutK8s, "", "-n", ns, "wait", "--for=condition=Ready", "pod/"+gitSrvName, "--timeout="+gitSrvWait)
	if err != nil || code != 0 {
		return fmt.Errorf("the git server pod is not ready: %v %s", err, tail(out, 300))
	}
	return nil
}

// tarGz packs the files under dir into a gzip tar with paths relative to dir. It leaves out the sample hooks.
func tarGz(dir string) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		if d.IsDir() && d.Name() == "hooks" {
			return fs.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil || !info.Mode().IsRegular() {
			return err
		}
		raw, err := os.ReadFile(p) // #nosec G304 G122 -- a file of the temp dir that the harness made
		if err != nil {
			return err
		}
		_, err = tw.Write(raw)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const cloneContainer = "pm-clone"

// cloneInContainerSet works around https://github.com/argoproj/argo-workflows/issues/16144 for the run only. The
// executor cannot load an input git artifact onto the volume mount path of a containerSet: it renames the staged
// directory onto the mount and fails with "file exists". The harness removes such an artifact and adds a first
// container that clones the same repository and revision into the mount. The converted file on disk stays as written.
func cloneInContainerSet(docs []map[string]any) {
	for _, d := range docs {
		for _, tpl := range list(dig(d, "spec", "templates")) {
			cs, _ := tpl["containerSet"].(map[string]any)
			if cs == nil {
				continue
			}
			mounts := map[string]string{}
			for _, m := range list(cs["volumeMounts"]) {
				mounts[str(m, "mountPath")] = str(m, "name")
			}
			inputs, _ := tpl["inputs"].(map[string]any)
			var keep []any
			var clones []string
			for _, a := range list(dig(inputs, "artifacts")) {
				_, onMount := mounts[str(a, "path")]
				if a["git"] == nil || !onMount {
					keep = append(keep, a)
					continue
				}
				clones = append(clones, fmt.Sprintf("git clone -q %s /tmp/src && git -C /tmp/src checkout -q %s && cp -a /tmp/src/. %s/",
					shellQuote(str(a, "git", "repo")), shellQuote(str(a, "git", "revision")), shellQuote(str(a, "path"))))
			}
			if len(clones) == 0 {
				continue
			}
			if keep == nil {
				delete(inputs, "artifacts")
			} else {
				inputs["artifacts"] = keep
			}
			var roots []any
			for _, c := range list(cs["containers"]) {
				if c["dependencies"] == nil {
					c["dependencies"] = []any{cloneContainer}
				}
				roots = append(roots, c)
			}
			clone := map[string]any{
				"name": cloneContainer, "image": mirrorRef(gitSrvImage), "command": []any{"sh", "-ec"},
				"args": []any{strings.Join(clones, "\n")},
			}
			cs["containers"] = append([]any{clone}, roots...)
		}
	}
}

// shellQuote quotes s as one shell word.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
