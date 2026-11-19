package meta

import "io/fs"

func attach(files map[string][]byte, m *Meta) map[string][]byte {
	return AttachStack(files, &Stack{Layers: []*Meta{m}})
}

func embed(files map[string][]byte, m *Meta) map[string][]byte {
	return EmbedStack(files, &Stack{Layers: []*Meta{m}})
}

func load(fsys fs.FS, parsed []string) (*Meta, error) {
	s, err := LoadStack(fsys, parsed)
	if err != nil {
		return nil, err
	}
	return s.Top(), nil
}
