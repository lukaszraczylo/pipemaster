//go:build behave

package behave

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	tektonManifest = "tekton-v1.18.0.yaml"
	argoManifest   = "argo-v4.1.6.yaml"
	tektonCRD      = "pipelines.tekton.dev"
	argoCRD        = "workflows.argoproj.io"
	tektonNS       = "tekton-pipelines"
	argoNS         = "argo"
	nsPrefix       = "pm-behave"
	kubeShort      = time.Minute
	kubeLong       = 5 * time.Minute
	clusterStart   = 5 * time.Minute
	deleteTries    = 3
)

// Kube manages the Kiri Kubernetes cluster for the Tekton and Argo engines. It only ever talks to the kiri context.
type Kube struct {
	env      *Env
	open     map[string]struct{}
	counter  atomic.Int64
	runStamp int64
	mu       sync.Mutex
	nsMu     sync.Mutex
	started  bool
	ready    bool
}

// NewKube returns a manager; Start brings the cluster up.
func NewKube(env *Env) *Kube {
	return &Kube{env: env, runStamp: time.Now().Unix(), open: map[string]struct{}{}}
}

// Start checks the context, starts the cluster when it is down and installs the pinned Tekton and Argo releases.
func (k *Kube) Start(ctx context.Context) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ready {
		return nil
	}
	if err := k.env.RequireKiriContext(ctx); err != nil {
		return err
	}
	if err := k.ensureCluster(ctx); err != nil {
		return err
	}
	if err := k.checkDiskPressure(ctx); err != nil {
		return err
	}
	if err := k.install(ctx, tektonCRD, tektonManifest, "", tektonNS); err != nil {
		return err
	}
	if err := k.install(ctx, argoCRD, argoManifest, argoNS, argoNS); err != nil {
		return err
	}
	if err := k.checkDiskPressure(ctx); err != nil {
		return err
	}
	k.ready = true
	return nil
}

func (k *Kube) install(ctx context.Context, crd, manifest, createNS, waitNS string) error {
	if _, code, err := k.env.Kubectl(ctx, kubeShort, "", "get", "crd", crd); err == nil && code == 0 {
		return nil
	}
	if err := k.env.Require(filepath.Join(k.env.Tools, "manifests", manifest)); err != nil {
		return err
	}
	if createNS != "" {
		if out, code, err := k.env.Kubectl(ctx, kubeShort, "", "create", "namespace", createNS); err != nil || code != 0 {
			return fmt.Errorf("create namespace %s: %v %s", createNS, err, out)
		}
	}
	args := []string{"apply", "--server-side", "-f", filepath.Join(k.env.Tools, "manifests", manifest)}
	if createNS != "" {
		args = append(args, "-n", createNS)
	}
	if out, code, err := k.env.Kubectl(ctx, 10*time.Minute, "", args...); err != nil || code != 0 {
		return fmt.Errorf("install %s: %v %s", manifest, err, tail(out, 600))
	}
	out, code, err := k.env.Kubectl(ctx, 10*time.Minute, "", "-n", waitNS, "wait", "--for=condition=Available", "deployment", "--all", "--timeout=480s")
	if err != nil || code != 0 {
		return fmt.Errorf("wait for %s: %v %s", manifest, err, tail(out, 600))
	}
	return nil
}

// Stop stops the cluster when this run started it. It never deletes the cluster.
func (k *Kube) Stop(ctx context.Context) {
	k.sweep()
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.started {
		return
	}
	if err := k.env.RequireKiriContext(ctx); err != nil {
		return
	}
	_, _, _ = k.env.run(ctx, runOpts{Timeout: clusterStart}, "kiri", "k8s", "stop") // best effort: the run result stands either way
	k.started = false
}

// Namespace creates a fresh namespace for one run and returns it with its cleanup. The namespace is tracked before
// the create call, so a create that times out after the server accepted it is still deleted by the cleanup or by sweep.
func (k *Kube) Namespace(ctx context.Context) (string, func(), error) {
	ns := fmt.Sprintf("%s-%d-%d", nsPrefix, k.runStamp, k.counter.Add(1))
	k.nsMu.Lock()
	k.open[ns] = struct{}{}
	k.nsMu.Unlock()
	if out, code, err := k.env.Kubectl(ctx, kubeShort, "", "create", "namespace", ns); err != nil || code != 0 {
		k.release(ns)
		return "", nil, fmt.Errorf("create namespace %s: %v %s", ns, err, out)
	}
	return ns, func() { k.release(ns) }, nil
}

// release deletes one tracked namespace on a context that no cancel or timeout of the run can stop. It retries,
// because a namespace that stays behind is the failure this exists to prevent.
func (k *Kube) release(ns string) {
	for range deleteTries {
		c, cancel := context.WithTimeout(context.Background(), kubeShort)
		_, code, err := k.env.Kubectl(c, kubeShort, "", "delete", "namespace", ns, "--ignore-not-found", "--wait=false")
		cancel()
		if err == nil && code == 0 {
			k.nsMu.Lock()
			delete(k.open, ns)
			k.nsMu.Unlock()
			return
		}
	}
}

// sweep deletes every namespace of this run that a cleanup did not remove.
func (k *Kube) sweep() {
	k.nsMu.Lock()
	left := make([]string, 0, len(k.open))
	for ns := range k.open {
		left = append(left, ns)
	}
	k.nsMu.Unlock()
	for _, ns := range left {
		k.release(ns)
	}
}

// logLine is one timestamped container log line.
type logLine struct {
	at   time.Time
	text string
}

// collectLogs reads the logs of every pod that matches the selector, merged by timestamp.
func (k *Kube) collectLogs(ctx context.Context, ns, selector string) (string, []string) {
	out, _, err := k.env.Kubectl(ctx, kubeLong, "", "-n", ns, "logs", "-l", selector, "--all-containers", "--timestamps", "--prefix=false", "--max-log-requests=50", "--tail=-1", "--ignore-errors")
	if err != nil {
		return err.Error(), nil
	}
	var lines []logLine
	for _, l := range strings.Split(out, "\n") {
		ts, rest, ok := strings.Cut(l, " ")
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			continue
		}
		lines = append(lines, logLine{at: at, text: rest})
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].at.Before(lines[j].at) })
	text := make([]string, len(lines))
	for i, l := range lines {
		text[i] = l.text
	}
	return out, text
}

// waitStatus polls a jsonpath until it prints one of the done values.
func (k *Kube) waitStatus(ctx context.Context, ns, object, jsonpath string, done map[string]int) (int, error) {
	deadline := time.Now().Add(timeoutK8s)
	for time.Now().Before(deadline) {
		out, code, err := k.env.Kubectl(ctx, kubeShort, "", "-n", ns, "get", object, "-o", "jsonpath="+jsonpath)
		if err == nil && code == 0 {
			if exit, ok := done[strings.TrimSpace(out)]; ok {
				return exit, nil
			}
		}
		if err := k.checkDiskPressure(ctx); err != nil {
			return -1, err
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return -1, fmt.Errorf("%s in %s did not finish within %s", object, ns, timeoutK8s)
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

const (
	clusterWaitTotal = 3 * time.Minute
	clusterWaitStep  = 5 * time.Second
)

var clusterDownRe = regexp.MustCompile(`(?i)exited|stopped|not running|not started`)

// ensureCluster uses a running cluster as it is, because another run can share it. It starts the cluster only when
// `kiri k8s status` says the cluster is down, and then it owns the stop.
func (k *Kube) ensureCluster(ctx context.Context) error {
	up := func() bool {
		_, code, err := k.env.Kubectl(ctx, kubeShort, "", "get", "nodes")
		return err == nil && code == 0
	}
	if up() {
		return nil
	}
	status, _, err := k.env.run(ctx, runOpts{Timeout: kubeShort}, "kiri", "k8s", "status")
	if err != nil {
		return fmt.Errorf("kiri k8s status: %w", err)
	}
	if clusterDownRe.MatchString(status) {
		out, code, err := k.env.run(ctx, runOpts{Timeout: clusterStart}, "kiri", "k8s", "start")
		if err != nil || code != 0 {
			return fmt.Errorf("kiri k8s start: %v %s", err, out)
		}
		k.started = true
		return k.env.RequireKiriContext(ctx)
	}
	deadline := time.Now().Add(clusterWaitTotal)
	for time.Now().Before(deadline) {
		if up() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(clusterWaitStep):
		}
	}
	return fmt.Errorf("the Kiri cluster is neither up nor down: %s", tail(status, 200))
}

// checkDiskPressure fails when the node carries the disk-pressure taint, because the scheduler then places no pod.
func (k *Kube) checkDiskPressure(ctx context.Context) error {
	out, code, err := k.env.Kubectl(ctx, kubeShort, "", "get", "nodes", "-o", "jsonpath={.items[*].spec.taints[*].key}")
	if err != nil || code != 0 {
		return fmt.Errorf("read the node taints: %v %s", err, tail(out, 200))
	}
	if strings.Contains(out, "disk-pressure") {
		return fmt.Errorf("the Kiri node has the disk-pressure taint, so the scheduler places no pod; free disk on the Kiri VM")
	}
	return nil
}
