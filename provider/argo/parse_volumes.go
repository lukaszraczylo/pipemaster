package argo

import (
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	// wsRoot holds one directory per Argo volume, relative to the checkout of a job.
	wsRoot = "pm-workspaces"
	// volStrategy is the strategy of the verdicts of a volume that the job reads as a directory.
	volStrategy = "workspace directory"
	// cachePrefix starts the name of the claim that the emitter writes for a job cache.
	cachePrefix                              = "cache-"
	kindEmptyDir, kindClaimTemplate, kindPVC = "emptyDir", "volumeClaimTemplate", "persistentVolumeClaim"
)

// volume is a volume that a workflow or a template declares.
type volume struct {
	name, kind, object string
}

// externalKinds name the volume sources that the operator provides as a cluster object.
var externalKinds = map[string]string{
	"secret": "Secret", "configMap": "ConfigMap", kindPVC: "PersistentVolumeClaim", "projected": "projected volume", "csi": "CSI volume",
}

// objectKeys read the name of the object from the key of each source.
var objectKeys = map[string]string{"secret": "secretName", "configMap": "name", kindPVC: "claimName"}

// shared is true for a volume that more than one template can mount, so the tasks of the workflow can pass files through it.
func (v volume) shared() bool { return v.kind == kindClaimTemplate || v.kind == kindPVC }

// passesFiles is true for the sources that tasks write.
func (v volume) passesFiles() bool { return v.kind == kindEmptyDir || v.shared() }

func (v volume) external() (string, bool) {
	what, ok := externalKinds[v.kind]
	if !ok {
		return "", false
	}
	if v.object != "" {
		what += " " + v.object
	}
	return what, true
}

// mount is one volume mount of a container.
type mount struct{ name, path, sub string }

// volumeList reads the volumes of a list: the key of the source names the kind.
func volumeList(v any) []volume {
	var out []volume
	for _, e := range listOf(v) {
		m, ok := asMap(e)
		name, _ := text(valueOf(m, "name"))
		if !ok || name == "" {
			continue
		}
		vol := volume{name: name}
		for _, k := range m.Order {
			if k == "name" {
				continue
			}
			vol.kind = k
			if key, ok := objectKeys[k]; ok {
				vol.object, _ = text(valueOf(asMapOr(m.Vals[k]), key))
			}
			break
		}
		out = append(out, vol)
	}
	return out
}

// workflowVolumes reads the claim templates and the volumes of the workflow spec.
func (c *plCtx) workflowVolumes() map[string]volume {
	if c.vols != nil {
		return c.vols
	}
	c.vols = map[string]volume{}
	for _, e := range listOf(valueOf(c.src.spec, "volumeClaimTemplates")) {
		name, _ := text(valueOf(asMapOr(valueOf(asMapOr(e), "metadata")), "name"))
		if name != "" {
			c.vols[name] = volume{name: name, kind: kindClaimTemplate}
		}
	}
	for _, v := range volumeList(valueOf(c.src.spec, "volumes")) {
		c.vols[v.name] = v
	}
	return c.vols
}

// mountsOf lists the volume mounts of the containers of a template.
func mountsOf(t *omap) []mount {
	var out []mount
	add := func(v any) {
		for _, e := range listOf(v) {
			m := asMapOr(e)
			name, _ := text(valueOf(m, "name"))
			p, _ := text(valueOf(m, "mountPath"))
			sub, _ := text(valueOf(m, "subPath"))
			if name != "" && p != "" {
				out = append(out, mount{name: name, path: p, sub: sub})
			}
		}
	}
	for _, kind := range []string{kindContainer, kindScript} {
		add(valueOf(asMapOr(valueOf(t, kind)), "volumeMounts"))
	}
	cs := asMapOr(valueOf(t, kindContainerSet))
	add(valueOf(cs, "volumeMounts"))
	for _, e := range listOf(valueOf(cs, "containers")) {
		add(valueOf(asMapOr(e), "volumeMounts"))
	}
	return out
}

// pathPair maps the mount path of a volume to its directory in the checkout.
type pathPair struct{ path, dir string }

// planVolumes maps each volume that the template mounts to a directory of the checkout. The emitter's own
// workspace and cache mounts, an unknown volume and a subPath that leaves the volume stay unmapped. A mount that
// shares its directory with another path, or its path with another directory, stays unmapped: the text has no
// spelling to tell the two apart on the way back.
func (b *jobBuilder) planVolumes(t *omap) {
	vols := maps.Clone(b.c.workflowVolumes())
	for _, v := range volumeList(valueOf(t, "volumes")) {
		vols[v.name] = v
	}
	b.vols, b.mapped = map[string]volume{}, map[mount]bool{}
	var pairs []pathPair
	for _, m := range mountsOf(t) {
		v, ok := vols[m.name]
		if !ok || m.name == workspaceName || strings.HasPrefix(m.name, cachePrefix) || m.path == "/" {
			continue
		}
		dir := path.Join(wsRoot, v.name)
		if m.sub != "" {
			clean := path.Clean(m.sub)
			if strings.Contains(m.sub, tagOpen) || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
				continue
			}
			dir = path.Join(dir, clean)
		}
		if i := slices.IndexFunc(pairs, func(p pathPair) bool { return p.path != m.path && p.dir == dir || p.path == m.path && p.dir != dir }); i >= 0 {
			b.keepMountPath(m, dir, pairs[i])
			continue
		}
		b.vols[v.name], b.mapped[m] = v, true
		b.c.mounted[b.job.ID] = append(b.c.mounted[b.job.ID], v.name)
		if v.shared() {
			b.c.binds[b.job.ID] = append(b.c.binds[b.job.ID], v.name)
		}
		if !slices.ContainsFunc(pairs, func(p pathPair) bool { return p.path == m.path }) {
			pairs = append(pairs, pathPair{path: m.path, dir: dir})
		}
	}
	slices.SortStableFunc(pairs, func(a, b pathPair) int { return len(b.path) - len(a.path) })
	b.tx.paths = pairs
}

// keepMountPath reports a mount that stays unmapped because it conflicts with a mapped one.
func (b *jobBuilder) keepMountPath(m mount, dir string, with pathPair) {
	why := "the volume " + m.name + " is also mounted at " + with.path + ", which already stands for " + dir
	if m.path == with.path {
		why = "another volume is mounted at " + m.path + " and already stands for " + with.dir
	}
	b.c.p.rep.Add(report.Entry{
		IRPath: b.k.Path, Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "mount path stays as written", SourceLoc: b.c.file,
		Detail: "the mount of the volume " + m.name + " at " + m.path + " has no directory of its own: " + why + ", so a script that reads " + m.path + " keeps that path, which no artifact carries to a later job on the target",
	})
}

// addVolumeDirs gives a job that mounts volumes a first step that creates their directories. Argo mounts the
// volume, so the Argo emitter skips the step.
func (b *jobBuilder) addVolumeDirs() {
	if len(b.tx.paths) == 0 || len(b.job.Steps) == 0 {
		return
	}
	var dirs []string
	for _, p := range b.tx.paths {
		dirs = append(dirs, p.dir)
	}
	b.job.Steps = append([]*ir.Step{emitkit.DirsStep(b.job.ID, dirs, dialect, ExtWorkspaceDirs)}, b.job.Steps...)
}

// keepVolumes keeps the template volumes in Ext. A volume that no mount maps has no directory and is lost.
func (b *jobBuilder) keepVolumes(v any, line int) {
	b.k.Out["volumes"] = plain(v)
	var rest []string
	for _, vol := range volumeList(v) {
		if _, ok := b.vols[vol.name]; !ok {
			rest = append(rest, vol.name)
		}
	}
	if len(rest) > 0 {
		b.k.Note(caps.ExtPassthrough, "volumes "+strings.Join(rest, ", ")+" have no IR field and no container mount maps them to a directory; kept in Ext", line)
	}
}

// keepMounts keeps the volume mounts of a container in Ext. A mount that is not mapped is lost.
func (b *jobBuilder) keepMounts(k *sink, v any, line int) {
	k.Out["volumeMounts"] = plain(v)
	if b.allMapped(v) {
		return
	}
	k.Note(caps.ExtPassthrough, "key volumeMounts has no IR field for a volume that no directory stands for; kept in Ext", line)
}

// allMapped is true when every mount of the list is mapped to a directory.
func (b *jobBuilder) allMapped(v any) bool {
	for _, e := range listOf(v) {
		m := asMapOr(e)
		name, _ := text(valueOf(m, "name"))
		p, _ := text(valueOf(m, "mountPath"))
		sub, _ := text(valueOf(m, "subPath"))
		if !b.mapped[mount{name: name, path: p, sub: sub}] {
			return false
		}
	}
	return true
}

// reportVolumes gives each mapped volume of the job its verdict.
func (b *jobBuilder) reportVolumes() {
	for _, name := range slices.Sorted(maps.Keys(b.vols)) {
		v := b.vols[name]
		dir := path.Join(wsRoot, name)
		level, strategy := caps.Emulated, volStrategy
		detail := "volume " + name + " is the directory " + dir + " of the job checkout; its mount paths in scripts, commands and working directories read that directory"
		if what, ok := v.external(); ok {
			level, strategy = caps.External, "volume bound to a "+what
			detail = "volume " + name + " is a " + what + "; the operator must provide " + what + " and put its files in " + dir +
				" before the step runs: a GitHub Actions secret written to the files, a GitLab file CI/CD variable, or a Drone or Woodpecker secret written to the files"
			if v.kind == kindPVC {
				detail += "; a PersistentVolumeClaim keeps data between runs, and the target keeps it only as an artifact between jobs of one run"
			}
		}
		b.c.p.rep.Add(report.Entry{IRPath: b.path, Feature: caps.ExtPassthrough, Level: level, Strategy: strategy, Detail: detail, SourceLoc: b.c.file})
	}
}

// keepWorkflowVolumes keeps the claim templates and volumes of the workflow spec in Ext. A claim that no template
// mounts has no directory and is lost.
func (c *plCtx) keepWorkflowVolumes(r *reader) {
	used := map[string]bool{}
	for _, names := range c.mounted {
		for _, n := range names {
			used[n] = true
		}
	}
	for _, key := range []string{"volumeClaimTemplates", "volumes"} {
		v, ok := r.Take(key)
		if !ok {
			continue
		}
		c.k.Out[key] = plain(v)
		var rest []string
		if key == "volumes" {
			for _, vol := range volumeList(v) {
				if !used[vol.name] {
					rest = append(rest, vol.name)
				}
			}
		} else {
			for _, e := range listOf(v) {
				name, _ := text(valueOf(asMapOr(valueOf(asMapOr(e), "metadata")), "name"))
				if !used[name] && !strings.HasPrefix(name, cachePrefix) {
					rest = append(rest, name)
				}
			}
		}
		if len(rest) > 0 {
			c.k.Note(caps.ExtPassthrough, "key "+key+" "+strings.Join(rest, ", ")+" has no IR field and no template mounts it to a directory; kept in Ext", r.Line(key))
		}
	}
}

// linkVolumes turns the volumes that tasks share into artifacts.
func (c *plCtx) linkVolumes(jobs []*ir.Job) {
	dirs := map[string]string{}
	for _, v := range c.workflowVolumes() {
		dirs[v.name] = path.Join(wsRoot, v.name)
	}
	emitkit.SharedDirs{
		Dir: dirs, Binds: c.binds, Dialect: dialect, Key: ExtWorkspaceArtifacts, Noun: "task", Medium: "the volume", Pipeline: c.plID, File: c.file,
	}.Link(jobs, c.p.rep)
}

// mapPaths rewrites each mount path in s to the directory of its volume. A mount path that the text holds as a
// whole path segment is rewritten; the pair is recorded so the Argo file gets the mount path back.
func (x *texts) mapPaths(s string) string {
	if strings.Contains(s, wsRoot+"/") {
		return s
	}
	for _, p := range x.paths {
		var out strings.Builder
		rest, hit := s, false
		for {
			i := strings.Index(rest, p.path)
			if i < 0 {
				break
			}
			end := i + len(p.path)
			if (i == 0 || !pathChar(rest[i-1])) && (end == len(rest) || !pathChar(rest[end]) || rest[end] == '/') {
				out.WriteString(rest[:i] + path.Clean(x.up+p.dir))
				hit = true
			} else {
				out.WriteString(rest[:end])
			}
			rest = rest[end:]
		}
		if hit {
			x.hits[p.dir] = p.path
			s = out.String() + rest
		}
	}
	return s
}

// pathChar is true for a byte that continues a path segment.
func pathChar(c byte) bool {
	return c == '_' || c == '.' || c == '-' || c == '/' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// keepMountPaths records the rewritten mount paths of the job, so the emitter writes them back.
func (b *jobBuilder) keepMountPaths() {
	if len(b.tx.hits) == 0 {
		return
	}
	out := map[string]any{}
	for dir, p := range b.tx.hits {
		out[dir] = p
	}
	b.k.Out[ExtMountPaths] = out
}
