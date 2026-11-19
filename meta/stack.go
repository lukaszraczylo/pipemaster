package meta

import (
	"errors"
	"fmt"
	"io/fs"

	"go.yaml.in/yaml/v3"
)

// Stack is the round-trip record of a chain of conversions, oldest layer first.
type Stack struct {
	Layers []*Meta
}

// Top returns the newest layer, or nil for an empty stack.
func (s *Stack) Top() *Meta {
	if s == nil || len(s.Layers) == 0 {
		return nil
	}
	return s.Layers[len(s.Layers)-1]
}

// Find returns the index of the layer nearest the top whose Source is source, or -1.
func (s *Stack) Find(source string) int {
	for i := len(s.Layers) - 1; i >= 0; i-- {
		if s.Layers[i].Source == source {
			return i
		}
	}
	return -1
}

// stackDoc is the version 2 document. A version 1 document is one Meta at the root.
type stackDoc struct {
	Layers  []*Meta `yaml:"layers"`
	Version int     `yaml:"version"`
}

// LoadStack reads the sidecar, else the first embedded block in the parsed files, in sorted order. A block
// counts only when its top layer's files list names the file that holds it. It returns an empty stack when
// neither exists.
func LoadStack(fsys fs.FS, parsed []string) (*Stack, error) {
	b, err := fs.ReadFile(fsys, SidecarPath)
	switch {
	case err == nil:
		return parseStack(b)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("read %s: %w", SidecarPath, err)
	}
	return loadEmbedded(fsys, parsed)
}

// AttachStack records the top layer's file hashes and returns a copy of files plus the sidecar.
// It writes nothing for an empty stack.
func AttachStack(files map[string][]byte, s *Stack) map[string][]byte {
	out := make(map[string][]byte, len(files)+1)
	for k, v := range files {
		out[k] = v
	}
	delete(out, SidecarPath)
	if len(s.Layers) == 0 {
		return out
	}
	s.Top().Files = hashAll(out)
	out[SidecarPath] = marshalStack(s)
	return out
}

func parseStack(b []byte) (*Stack, error) {
	var probe struct {
		Version int `yaml:"version"`
	}
	if err := yaml.Unmarshal(b, &probe); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}
	switch probe.Version {
	case legacyVersion:
		var m Meta
		if err := yaml.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("parse metadata: %w", err)
		}
		return &Stack{Layers: []*Meta{normalise(&m)}}, nil
	case Version:
		var d stackDoc
		if err := yaml.Unmarshal(b, &d); err != nil {
			return nil, fmt.Errorf("parse metadata: %w", err)
		}
		s := &Stack{}
		for _, m := range d.Layers {
			if m == nil {
				return nil, errors.New("parse metadata: empty layer")
			}
			s.Layers = append(s.Layers, normalise(m))
		}
		return s, nil
	default:
		return nil, fmt.Errorf("unsupported metadata version %d, want %d or %d", probe.Version, legacyVersion, Version)
	}
}

// normalise makes a loaded layer equal a collected one: yaml decodes values nested under an ir.Ext key as
// ir.Ext, and the layer carries the current version in memory.
func normalise(m *Meta) *Meta {
	for path, e := range m.Ext {
		m.Ext[path] = copyExt(e)
	}
	m.Version = Version
	return m
}

func marshalStack(s *Stack) []byte {
	d := stackDoc{Version: Version}
	for _, m := range s.Layers {
		c := *m
		c.Version = 0 // the document carries the version
		d.Layers = append(d.Layers, &c)
	}
	b, err := yaml.Marshal(d)
	if err != nil {
		// Layers hold only maps, strings and YAML-decoded values, so this cannot fail.
		panic(fmt.Sprintf("meta: marshal: %v", err))
	}
	return b
}
