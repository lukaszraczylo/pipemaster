package roundtrip_test

import (
	"io/fs"

	"github.com/lukaszraczylo/pipemaster/meta"
)

func attachOne(files map[string][]byte, m *meta.Meta) map[string][]byte {
	return meta.AttachStack(files, &meta.Stack{Layers: []*meta.Meta{m}})
}

func embedOne(files map[string][]byte, m *meta.Meta) map[string][]byte {
	return meta.EmbedStack(files, &meta.Stack{Layers: []*meta.Meta{m}})
}

func loadTop(fsys fs.FS, parsed []string) (*meta.Meta, error) {
	s, err := meta.LoadStack(fsys, parsed)
	if err != nil {
		return nil, err
	}
	return s.Top(), nil
}
