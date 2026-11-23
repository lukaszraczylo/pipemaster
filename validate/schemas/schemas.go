// Package schemas embeds the pinned provider JSON Schemas.
package schemas

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
	"time"
)

const gzSuffix = ".gz"

// GitHubWorkflow is the GitHub Actions workflow schema.
//
//go:embed github-workflow.json
var GitHubWorkflow []byte

// GitLabCI is the GitLab CI schema.
//
//go:embed gitlab-ci.json
var GitLabCI []byte

// Woodpecker is the Woodpecker CI workflow schema.
//
//go:embed woodpecker.json
var Woodpecker []byte

// Drone is the Drone CI pipeline schema.
//
//go:embed drone.json
var Drone []byte

//go:embed kubernetes-definitions.json.gz
var kubernetesDefinitionsGz []byte

// KubernetesDefinitions is the Kubernetes schema file that drone.json references. It is stored gzip-compressed.
var KubernetesDefinitions = mustGunzip("kubernetes-definitions.json.gz", kubernetesDefinitionsGz)

//go:embed tekton/*.json.gz
var tektonGz embed.FS

//go:embed argo/*.json.gz
var argoGz embed.FS

// Tekton holds the generated Tekton CRD schemas, the batch/v1 CronJob schema and dispatch.json, which routes a
// document to its schema by apiVersion and kind. It serves tekton/<name>.json from gzip-compressed files.
var Tekton fs.FS = &gzFS{src: tektonGz}

// Argo holds the Argo Workflows and Argo Events CRD schemas and dispatch.json, which routes a document to its
// schema by apiVersion and kind. It serves argo/<name>.json from gzip-compressed files.
var Argo fs.FS = &gzFS{src: argoGz}

// Concourse is the Concourse pipeline schema, written by hand from the v8.3.3 Go structs.
//
//go:embed concourse.json
var Concourse []byte

// mustGunzip panics on a corrupt embedded file: that is a build defect.
func mustGunzip(name string, data []byte) []byte {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		panic("schemas: " + name + ": " + err.Error())
	}
	out, err := io.ReadAll(r)
	if err != nil {
		panic("schemas: " + name + ": " + err.Error())
	}
	return out
}

// gzFS serves the .json.gz files of src as decompressed .json files. It decompresses all files once, on first read.
type gzFS struct {
	data map[string][]byte
	src  embed.FS
	once sync.Once
}

func (g *gzFS) load() {
	g.once.Do(func() {
		g.data = map[string][]byte{}
		err := fs.WalkDir(g.src, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := g.src.ReadFile(p)
			if err != nil {
				return err
			}
			g.data[strings.TrimSuffix(p, gzSuffix)] = mustGunzip(p, b)
			return nil
		})
		if err != nil {
			panic("schemas: " + err.Error())
		}
	})
}

func (g *gzFS) ReadFile(name string) ([]byte, error) {
	g.load()
	b, ok := g.data[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), b...), nil
}

func (g *gzFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := g.src.ReadDir(name)
	if err != nil {
		return nil, err
	}
	g.load()
	out := make([]fs.DirEntry, 0, len(entries))
	for _, e := range entries {
		n := strings.TrimSuffix(e.Name(), gzSuffix)
		out = append(out, fs.FileInfoToDirEntry(&fileInfo{name: n, size: int64(len(g.data[path.Join(name, n)])), dir: e.IsDir()}))
	}
	return out, nil
}

func (g *gzFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if b, err := g.ReadFile(name); err == nil {
		return &memFile{Reader: bytes.NewReader(b), info: fileInfo{name: path.Base(name), size: int64(len(b))}}, nil
	}
	entries, err := g.ReadDir(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &dirFile{info: fileInfo{name: path.Base(name), dir: true}, entries: entries}, nil
}

type fileInfo struct {
	name string
	size int64
	dir  bool
}

func (f *fileInfo) Name() string { return f.name }
func (f *fileInfo) Size() int64  { return f.size }
func (f *fileInfo) Mode() fs.FileMode {
	if f.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (f *fileInfo) ModTime() time.Time { return time.Time{} }
func (f *fileInfo) IsDir() bool        { return f.dir }
func (f *fileInfo) Sys() any           { return nil }

type memFile struct {
	*bytes.Reader
	info fileInfo
}

func (f *memFile) Stat() (fs.FileInfo, error) { return &f.info, nil }
func (f *memFile) Close() error               { return nil }

type dirFile struct {
	entries []fs.DirEntry
	info    fileInfo
}

func (d *dirFile) Stat() (fs.FileInfo, error) { return &d.info, nil }
func (d *dirFile) Close() error               { return nil }
func (d *dirFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.name, Err: fs.ErrInvalid}
}

func (d *dirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		out := d.entries
		d.entries = nil
		return out, nil
	}
	if len(d.entries) == 0 {
		return nil, io.EOF
	}
	n = min(n, len(d.entries))
	out := d.entries[:n]
	d.entries = d.entries[n:]
	return out, nil
}
