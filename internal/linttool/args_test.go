package linttool

import (
	"slices"
	"testing"
)

func TestLintArgsAllowEveryPrivilegedPlugin(t *testing.T) {
	args := lintArgs([]string{"--x"}, "f.yaml")
	for _, plug := range []string{"woodpeckerci/plugin-docker-buildx", "plugins/docker"} {
		i := slices.Index(args, plug)
		if i < 1 || args[i-1] != "--plugins-privileged" {
			t.Errorf("%s is not passed as a privileged plugin: %v", plug, args)
		}
	}
	if args[len(args)-1] != "f.yaml" || !slices.Contains(args, "--x") {
		t.Errorf("the extra flag and the file must stay: %v", args)
	}
}
