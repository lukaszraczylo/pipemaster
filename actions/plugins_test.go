package actions

import (
	"slices"
	"strings"
	"testing"
)

func TestLookupPlugin(t *testing.T) {
	tests := []struct {
		ref  string
		want []string
	}{
		{"plugins/slack", []string{"/bin/drone-slack"}},
		{"plugins/slack:1.2", []string{"/bin/drone-slack"}},
		{"docker.io/plugins/slack@sha256:abc", []string{"/bin/drone-slack"}},
		{"Plugins/Slack:latest", []string{"/bin/drone-slack"}},
		{"woodpeckerci/plugin-docker-buildx:5.0.0", []string{"/usr/local/bin/dockerd-entrypoint.sh", "plugin-docker-buildx"}},
		{"localhost:5000/plugins/slack", nil},
		{"plugins/unknown", nil},
		{"", nil},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			p, ok := LookupPlugin(tt.ref)
			if (tt.want != nil) != ok || !slices.Equal(p.Entrypoint, tt.want) {
				t.Fatalf("LookupPlugin(%q) = %v, %v; want %v", tt.ref, p.Entrypoint, ok, tt.want)
			}
		})
	}
}

func TestPluginTableIsComplete(t *testing.T) {
	tbl, err := loadPlugins()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugins/docker", "plugins/s3", "plugins/slack", "plugins/webhook", "plugins/git", "plugins/manifest",
		"plugins/gcr", "plugins/ecr", "plugins/github-release", "plugins/gitea-release", "plugins/downstream", "appleboy/drone-ssh",
		"appleboy/drone-scp", "woodpeckerci/plugin-docker-buildx", "woodpeckerci/plugin-git", "woodpeckerci/plugin-s3",
		"woodpeckerci/plugin-ready-release-go"} {
		p, ok := tbl[name]
		if !ok || !strings.HasPrefix(p.Source, "https://github.com/") {
			t.Errorf("%s: missing or no source", name)
		}
	}
}
