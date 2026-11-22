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

func TestKubeconformArgsAreStrictAndOffline(t *testing.T) {
	args := kubeconformArgs("/s", "f.yaml")
	if !slices.Contains(args, "-strict") || args[len(args)-1] != "f.yaml" {
		t.Errorf("want -strict and the file last: %v", args)
	}
	for _, a := range args {
		if a == "default" || a == "-ignore-missing-schemas" {
			t.Errorf("the arguments must neither use the network nor skip missing schemas: %v", args)
		}
	}
}

func TestArgoLintArgsAreOffline(t *testing.T) {
	args := argoLintArgs("f.yaml")
	if !slices.Contains(args, "--offline") || args[len(args)-1] != "f.yaml" {
		t.Errorf("want --offline and the file last: %v", args)
	}
}
