// Package meta stores what a provider file cannot hold, so converting A to B and back to A is exact.
package meta

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"sort"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// Version is the only metadata schema version this package reads.
const Version = 1

// SidecarPath is where Attach writes the metadata file.
const SidecarPath = ".pipemaster/meta.yaml"

// ErrStale means emitted files changed after the metadata was written.
var ErrStale = errors.New("round-trip metadata is stale")

// Meta is the round-trip record written next to, or inside, emitted files.
type Meta struct {
	Files map[string]string `yaml:"files,omitempty"` // path to sha256 hex
	Ext   map[string]ir.Ext `yaml:"ext,omitempty"`   // IR path to Ext
	IDs   map[string]string `yaml:"ids,omitempty"`   // emitted id to source id
	// Residual holds display names and step content the target cannot hold, by source IR path.
	Residual map[string]Residual `yaml:"residual,omitempty"`
	// Lost holds the source fields of features the emit report called lost or external, by source IR path.
	Lost map[string]Lost `yaml:"lost,omitempty"`
	// Emitted holds what the target file parsed to for the fields in Lost. A restore of an edited file applies a
	// stored field only where the file still reads as emitted.
	Emitted map[string]Lost `yaml:"emitted,omitempty"`
	Source  string          `yaml:"source,omitempty"`
	Dialect string          `yaml:"dialect,omitempty"`
	Version int             `yaml:"version"`
}

// Collect copies the Ext of every IR node that has one, keyed by IR path.
func Collect(p *ir.Project) map[string]ir.Ext {
	out := map[string]ir.Ext{}
	for _, n := range ir.ExtNodes(p) {
		if len(*n.Ext) > 0 {
			out[n.Path] = copyExt(*n.Ext)
		}
	}
	return out
}

// Reattach restores stored Ext into p. Stored keys overwrite node keys; other node keys stay.
// Residual fills only what the parsed node left empty. It returns the sorted paths in m that match no node.
// IDs are not applied here.
func Reattach(p *ir.Project, m *Meta) []string {
	orphans, _ := reattach(p, m, false)
	return orphans
}

// ReattachKeepingEdits is Reattach for a file that changed after conversion: a stored value never replaces a
// value the parsed node already holds. It returns the orphaned paths and the sorted paths where a stored
// value differed from the parsed one and was not applied.
func ReattachKeepingEdits(p *ir.Project, m *Meta) (orphans, kept []string) {
	return reattach(p, m, true)
}

func reattach(p *ir.Project, m *Meta, keepEdits bool) (orphans, kept []string) {
	found := map[string]bool{}
	keptSet := map[string]bool{}
	var lostApplied map[string]bool
	// Metadata with no emitted-form record cannot tell an edit from the emitter's own output, so an edited
	// file keeps every node that has lost fields as parsed.
	legacy := keepEdits && len(m.Lost) > 0 && len(m.Emitted) == 0
	if keepEdits {
		// The comparison with the emitted form must see the nodes before Ext changes their triggers.
		var lostKept map[string]bool
		if legacy {
			lostApplied, lostKept = skipLost(p, m)
		} else {
			lostApplied, lostKept = applyLost(p, m, true)
		}
		for path := range lostKept {
			keptSet[path] = true
		}
	}
	for _, n := range ir.ExtNodes(p) {
		stored, ok := m.Ext[n.Path]
		if !ok {
			continue
		}
		found[n.Path] = true
		if _, hasLost := m.Lost[n.Path]; legacy && hasLost {
			continue
		}
		if *n.Ext == nil {
			*n.Ext = ir.Ext{}
		}
		for k, v := range stored {
			if cur, has := (*n.Ext)[k]; keepEdits && has {
				if !reflect.DeepEqual(cur, copyValue(v)) {
					keptSet[n.Path] = true
				}
				continue
			}
			(*n.Ext)[k] = copyValue(v)
		}
	}
	if !keepEdits {
		lostApplied, _ = applyLost(p, m, false)
	}
	for _, applied := range []map[string]bool{applyResidual(p, m.Residual), lostApplied} {
		for path := range applied {
			found[path] = true
		}
	}
	for _, paths := range []map[string]bool{keySet(m.Ext), keySet(m.Residual), keySet(m.Lost)} {
		for path := range paths {
			if !found[path] && !slices.Contains(orphans, path) {
				orphans = append(orphans, path)
			}
		}
	}
	sort.Strings(orphans)
	return orphans, sortedKeys(keptSet)
}

// Attach records hashes of files in m and returns a copy of files plus the sidecar.
func Attach(files map[string][]byte, m *Meta) map[string][]byte {
	out := make(map[string][]byte, len(files)+1)
	for k, v := range files {
		out[k] = v
	}
	delete(out, SidecarPath)
	m.Files = hashAll(out)
	out[SidecarPath] = mustMarshal(m)
	return out
}

// Stale lists files in m that are missing or whose content changed, wrapping ErrStale when any exist.
func (m *Meta) Stale(fsys fs.FS) ([]string, error) {
	var stale []string
	for path, want := range m.Files {
		b, err := fs.ReadFile(fsys, path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			stale = append(stale, path)
		case err != nil:
			return nil, fmt.Errorf("read %s: %w", path, err)
		case hash(StripEmbedded(b)) != want:
			stale = append(stale, path)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		return stale, fmt.Errorf("%w: %v", ErrStale, stale)
	}
	return nil, nil
}

// Load reads the sidecar, else the first embedded block in the parsed files, in sorted order. A block counts
// only when its files list names the file that holds it. It returns nil, nil when neither exists.
func Load(fsys fs.FS, parsed []string) (*Meta, error) {
	b, err := fs.ReadFile(fsys, SidecarPath)
	switch {
	case err == nil:
		return parse(b)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("read %s: %w", SidecarPath, err)
	}
	return loadEmbedded(fsys, parsed)
}

func parse(b []byte) (*Meta, error) {
	var m Meta
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}
	if m.Version != Version {
		return nil, fmt.Errorf("unsupported metadata version %d, want %d", m.Version, Version)
	}
	// yaml decodes values nested under an ir.Ext key as ir.Ext; normalise so a loaded Meta equals a collected one.
	for path, e := range m.Ext {
		m.Ext[path] = copyExt(e)
	}
	return &m, nil
}

func mustMarshal(m *Meta) []byte {
	b, err := yaml.Marshal(m)
	if err != nil {
		// Meta holds only maps, strings and YAML-decoded values, so this cannot fail.
		panic(fmt.Sprintf("meta: marshal: %v", err))
	}
	return b
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hashAll(files map[string][]byte) map[string]string {
	out := make(map[string]string, len(files))
	for k, v := range files {
		out[k] = hash(v)
	}
	return out
}

func keySet[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func copyExt(e ir.Ext) ir.Ext {
	out := make(ir.Ext, len(e))
	for k, v := range e {
		out[k] = copyValue(v)
	}
	return out
}

func copyMap(x map[string]any) map[string]any {
	out := make(map[string]any, len(x))
	for k, e := range x {
		out[k] = copyValue(e)
	}
	return out
}

// copyValue deep-copies YAML-shaped values and maps nested ir.Ext to map[string]any, the form yaml decodes into elsewhere.
func copyValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return copyMap(x)
	case ir.Ext:
		return copyMap(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = copyValue(e)
		}
		return out
	default:
		return v
	}
}
