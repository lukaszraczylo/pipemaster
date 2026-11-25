package emitkit

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/provider"
)

// HostedKind says whether a runner label names a GitHub-hosted machine image and for which platform.
type HostedKind int

// Hosted label kinds.
const (
	NotHosted HostedKind = iota
	HostedLinux
	HostedMacOS
	HostedWindows
)

const exprOpen = "${{"

// Platform names the operating system of a hosted kind.
func (k HostedKind) Platform() string {
	switch k {
	case HostedLinux:
		return "Linux"
	case HostedMacOS:
		return "macOS"
	case HostedWindows:
		return "Windows"
	}
	return ""
}

// HostedRunner classifies a runner label. A hosted label names an image that only GitHub provides, so no
// other engine has a worker tag, label or node selector with that name by default.
func HostedRunner(label string) HostedKind {
	l := strings.ToLower(strings.TrimSpace(label))
	switch {
	case strings.HasPrefix(l, "ubuntu-"):
		return HostedLinux
	case strings.HasPrefix(l, "macos-"):
		return HostedMacOS
	case strings.HasPrefix(l, "windows-"):
		return HostedWindows
	}
	return NotHosted
}

// HostedHere classifies a label of a project that came from origin. Only a GHA-family project uses hosted
// labels; in any other dialect the same text names a real worker tag.
func HostedHere(origin, label string) HostedKind {
	if provider.Family(origin) != provider.GHA {
		return NotHosted
	}
	return HostedRunner(label)
}

// WorkerTags returns the labels that become a worker tag, label or node selector: not a hosted label of a GHA
// project, and not a label with a template expression, which names no worker.
func WorkerTags(origin string, labels []string) []string {
	var out []string
	for _, l := range labels {
		if !strings.Contains(l, exprOpen) && HostedHere(origin, l) == NotHosted {
			out = append(out, l)
		}
	}
	return out
}

// ReportHosted reports the hosted labels of a GHA project to a dialect that has no form for them. A hosted
// Linux label runs on any Linux worker, but the worker must supply the tools (external). A macOS or Windows
// label names a platform no worker stands for (lost). noun names what the dialect would have written, such as
// "worker tag". It returns the labels that stay as ordinary labels of the dialect.
func ReportHosted(origin string, labels []string, noun string, external func(strategy, detail string), lose func(detail string)) []string {
	hosted := map[HostedKind][]string{}
	for _, l := range labels {
		if k := HostedHere(origin, l); k != NotHosted && !strings.Contains(l, exprOpen) {
			hosted[k] = append(hosted[k], l)
		}
	}
	if h := hosted[HostedLinux]; len(h) > 0 {
		external("no "+noun, "the hosted label "+strings.Join(h, ", ")+" runs on any Linux worker; the GitHub-hosted image tools are not present")
	}
	for _, k := range []HostedKind{HostedMacOS, HostedWindows} {
		if h := hosted[k]; len(h) > 0 {
			lose("the hosted label " + strings.Join(h, ", ") + " names a " + k.Platform() + " machine image; no " + noun + " stands for it")
		}
	}
	return WorkerTags(origin, labels)
}
