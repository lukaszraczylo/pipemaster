package argo

import (
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// isCacheMounts is true for the mounts that the emitter writes for the job caches that the marker holds: the
// workspace, then one subPath of the claim of each cache for each of its paths. The emitter writes them again.
func (b *jobBuilder) isCacheMounts(v any) bool {
	var a cacheArg
	var caches []ir.Cache
	if !markerArg(b.c.src.doc.Head, b.path, caps.JobCache, &a) || yaml.Unmarshal([]byte(a.Caches), &caches) != nil {
		return false
	}
	want := [][3]string{{workspaceName, workspaceMount, ""}}
	for i, c := range caches {
		for k, p := range c.Paths {
			want = append(want, [3]string{"cache-" + b.cl.name + "-" + itoa(i+1), artifactPath(strings.TrimPrefix(p, "~/")), "p" + itoa(k+1)})
		}
	}
	list := listOf(v)
	if len(list) != len(want) {
		return false
	}
	for i, e := range list {
		m, ok := asMap(e)
		if !ok {
			return false
		}
		name, _ := text(m.Vals["name"])
		path, _ := text(m.Vals["mountPath"])
		sub, _ := text(m.Vals["subPath"])
		if name != want[i][0] || path != want[i][1] || sub != want[i][2] || len(m.Order) != 2+min(len(sub), 1) {
			return false
		}
	}
	return true
}
