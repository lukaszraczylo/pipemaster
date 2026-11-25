package actions

import (
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

//go:embed plugins.yaml
var pluginsEmbedded []byte

// Plugin is a plugin image with the command its entrypoint runs.
type Plugin struct {
	// Shell is false for an image that holds no shell (FROM scratch), so no script can run in it.
	Shell      *bool    `yaml:"shell"`
	Image      string   `yaml:"image"`
	Source     string   `yaml:"source"`
	Entrypoint []string `yaml:"entrypoint"`
	Privileged bool     `yaml:"privileged"`
}

// HasShell is false only when the table says the image holds no shell.
func (p Plugin) HasShell() bool { return p.Shell == nil || *p.Shell }

var (
	pluginOnce  sync.Once
	pluginTable map[string]Plugin
	pluginErr   error
)

func loadPlugins() (map[string]Plugin, error) {
	pluginOnce.Do(func() {
		var doc struct {
			Plugins []Plugin `yaml:"plugins"`
		}
		if pluginErr = yaml.Unmarshal(pluginsEmbedded, &doc); pluginErr != nil {
			pluginErr = fmt.Errorf("actions: plugins.yaml: %w", pluginErr)
			return
		}
		pluginTable = map[string]Plugin{}
		for _, p := range doc.Plugins {
			key := strings.ToLower(p.Image)
			if _, dup := pluginTable[key]; dup || len(p.Entrypoint) == 0 || p.Source == "" {
				pluginErr = fmt.Errorf("actions: plugins.yaml: entry %q is duplicate, has no entrypoint or has no source", p.Image)
				return
			}
			pluginTable[key] = p
		}
	})
	return pluginTable, pluginErr
}

// PluginName strips the registry host of Docker Hub, the digest and the tag from an image reference.
func PluginName(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	for _, host := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		ref = strings.TrimPrefix(ref, host)
	}
	return strings.ToLower(ref)
}

// LookupPlugin finds the entrypoint of a plugin image. The tag and the digest do not take part in the match.
func LookupPlugin(ref string) (Plugin, bool) {
	t, err := loadPlugins()
	if err != nil {
		panic(err.Error())
	}
	p, ok := t[PluginName(ref)]
	if ok {
		p.Entrypoint = append([]string(nil), p.Entrypoint...)
	}
	return p, ok
}
