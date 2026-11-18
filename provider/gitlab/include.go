package gitlab

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// maxIncludes is GitLab's limit on files in one pipeline.
const maxIncludes = 150

var yamlLineRE = regexp.MustCompile(`^yaml: (?:unmarshal errors:\s*)?(?:line (\d+): )?`)

// loader reads the root file and everything it includes locally.
type loader struct {
	*state
	fsys    fs.FS
	seen    map[string]bool
	args    map[ir.FeatureUse][]string
	ids     map[string]string
	files   []string
	remote  []any
	markers []ir.FeatureUse
}

func newLoader(fsys fs.FS, s *state) *loader {
	return &loader{state: s, fsys: fsys, seen: map[string]bool{}, args: map[ir.FeatureUse][]string{}, ids: map[string]string{}}
}

// load returns the merged configuration of name: included files first in order, then name itself on top.
func (l *loader) load(name string, chain []string, from *yaml.Node) (*yaml.Node, error) {
	if l.includes++; l.includes > maxIncludes {
		return nil, l.errf(from, "more than %d included files", maxIncludes)
	}
	if indexOf(chain, name) >= 0 {
		return nil, l.errf(from, "include cycle: %s", strings.Join(append(append([]string{}, chain...), name), " -> "))
	}
	src, err := fs.ReadFile(l.fsys, name)
	if err != nil {
		if from != nil {
			return nil, l.errf(from, "include %s: %v", name, errors.Unwrap(err))
		}
		return nil, fmt.Errorf("gitlab: read %s: %w", name, err)
	}
	if !l.seen[name] {
		l.seen[name] = true
		l.files = append(l.files, name)
		l.markers = append(l.markers, emitkit.ParseMarkers(src)...)
		for k, v := range jobIDMarkers(src) {
			l.ids[k] = v
		}
		for u, a := range markerArgs(src) {
			l.args[u] = append(l.args[u], a...)
		}
	}
	root, err := l.parseDoc(name, src)
	if err != nil {
		return nil, err
	}
	inc := get(root, "include")
	if inc == nil {
		return root, nil
	}
	var acc *yaml.Node
	ents, err := l.includeEntries(inc)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		for _, p := range e.paths {
			m, err := l.load(p, append(chain, name), e.node)
			if err != nil {
				return nil, err
			}
			acc = l.deepMerge(acc, m)
		}
	}
	return l.deepMerge(acc, l.withoutKey(root, "include")), nil
}

func indexOf(l []string, s string) int {
	for i, v := range l {
		if v == s {
			return i
		}
	}
	return -1
}

func (l *loader) parseDoc(name string, src []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return l.stamp(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, name), nil
		}
		return nil, yamlErr(name, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("%s:%d: a CI file holds one YAML document", name, extra.Line)
	} else if !errors.Is(err, io.EOF) {
		return nil, yamlErr(name, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return l.stamp(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, name), nil
	}
	root, err := l.clone(doc.Content[0], name, 0)
	if err != nil {
		return nil, err
	}
	if isNull(root) {
		return l.stamp(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, name), nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, l.errf(root, "the CI file must be a mapping")
	}
	return root, nil
}

// yamlErr rewrites a YAML syntax error as <file>:<line>: <message>.
func yamlErr(file string, err error) error {
	msg := err.Error()
	loc := yamlLineRE.FindStringSubmatch(msg)
	msg = strings.TrimSpace(yamlLineRE.ReplaceAllString(msg, ""))
	if loc != nil && loc[1] != "" {
		return fmt.Errorf("%s:%s: %s", file, loc[1], msg)
	}
	return fmt.Errorf("%s: %s", file, msg)
}

type includeEntry struct {
	node  *yaml.Node
	paths []string
}

var remoteKinds = []string{"remote", "project", "template", "component", "artifact"}

// includeEntries expands the include value into local file paths; every other kind goes to the Ext list.
func (l *loader) includeEntries(n *yaml.Node) ([]includeEntry, error) {
	var items []*yaml.Node
	switch n.Kind {
	case yaml.SequenceNode:
		items = n.Content
	default:
		items = []*yaml.Node{n}
	}
	var out []includeEntry
	for _, it := range items {
		e, err := l.includeEntry(it)
		if err != nil {
			return nil, err
		}
		if e != nil {
			out = append(out, *e)
		}
	}
	return out, nil
}

func (l *loader) includeEntry(it *yaml.Node) (*includeEntry, error) {
	switch it.Kind {
	case yaml.ScalarNode:
		if strings.HasPrefix(it.Value, "http://") || strings.HasPrefix(it.Value, "https://") {
			return nil, l.keepRemote(it, "remote")
		}
		return l.localEntry(it, it)
	case yaml.MappingNode:
		if v := get(it, "local"); v != nil {
			l.notEvaluated(it)
			return l.localEntry(it, v)
		}
		for _, k := range remoteKinds {
			if get(it, k) != nil {
				return nil, l.keepRemote(it, k)
			}
		}
	}
	return nil, l.errf(it, "include entry needs one of local, remote, project, template, component or artifact")
}

func (l *loader) notEvaluated(it *yaml.Node) {
	for _, k := range []string{"rules", "inputs"} {
		if get(it, k) != nil {
			l.rep.Add(report.Entry{
				IRPath: "pipeline/" + pipelineID, Feature: caps.ExtPassthrough, Level: caps.Lost,
				Strategy: "kept as merged text", Detail: "include " + k + " are not evaluated; the file is merged unconditionally", SourceLoc: l.loc(it),
			})
		}
	}
}

func (l *loader) keepRemote(it *yaml.Node, kind string) error {
	v, err := l.plain(it)
	if err != nil {
		return err
	}
	l.remote = append(l.remote, v)
	l.rep.Add(report.Entry{
		IRPath: "pipeline/" + pipelineID, Feature: caps.ExtPassthrough, Level: caps.Lost,
		Strategy: "kept in Ext[gitlab][include]", Detail: "remote include not followed (" + kind + ")", SourceLoc: l.loc(it),
	})
	return nil
}

func (l *loader) localEntry(entry, v *yaml.Node) (*includeEntry, error) {
	var pats []string
	switch v.Kind {
	case yaml.ScalarNode:
		pats = []string{v.Value}
	case yaml.SequenceNode:
		for _, c := range v.Content {
			if c.Kind != yaml.ScalarNode {
				return nil, l.errf(c, "include:local entries must be strings")
			}
			pats = append(pats, c.Value)
		}
	default:
		return nil, l.errf(v, "include:local must be a string or a list of strings")
	}
	e := &includeEntry{node: entry}
	for _, p := range pats {
		files, err := l.expand(entry, p)
		if err != nil {
			return nil, err
		}
		e.paths = append(e.paths, files...)
	}
	return e, nil
}

// expand resolves a repo-root-relative pattern to files; "*" and "**" match as GitLab documents.
func (l *loader) expand(at *yaml.Node, pat string) ([]string, error) {
	if slices.Contains(strings.Split(pat, "/"), "..") {
		return nil, l.errf(at, "include %q leaves the repository", pat)
	}
	clean := strings.TrimPrefix(path.Clean("/"+pat), "/")
	if !strings.ContainsAny(pat, "*?") {
		if clean == "" || !fs.ValidPath(clean) {
			return nil, l.errf(at, "include %q is not a file path", pat)
		}
		if st, err := fs.Stat(l.fsys, clean); err != nil || st.IsDir() {
			return nil, l.errf(at, "local include %s does not exist", clean)
		}
		return []string{clean}, nil
	}
	re, err := globRegexp(clean)
	if err != nil {
		return nil, l.errf(at, "include pattern %q: %v", pat, err)
	}
	var out []string
	err = fs.WalkDir(l.fsys, ".", func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if !d.IsDir() && re.MatchString(p) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, l.errf(at, "include pattern %q: %v", pat, err)
	}
	return out, nil
}

// globRegexp turns an include pattern into an anchored regexp: "**/" spans directories, "**" matches anything, "*" stays in one segment.
func globRegexp(pat string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
	for i := 0; i < len(pat); i++ {
		switch {
		case strings.HasPrefix(pat[i:], "**/"):
			sb.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(pat[i:], "**"):
			sb.WriteString(".*")
			i++
		case pat[i] == '*':
			sb.WriteString("[^/]*")
		case pat[i] == '?':
			sb.WriteString("[^/]")
		default:
			sb.WriteString(regexp.QuoteMeta(string(pat[i])))
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}
