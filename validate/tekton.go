package validate

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	"github.com/lukaszraczylo/pipemaster/validate/schemas"
)

const (
	tektonDir    = "tekton"
	tektonBase   = "https://pipemaster.invalid/schemas/tekton/"
	dispatchFile = "dispatch.json"
)

// kinds holds, per provider with per-document dispatch, the "apiVersion kind" pairs that have a schema.
var kinds = map[string]map[string]bool{}

// knownKind reports whether v is a document File must validate. Providers without per-document dispatch
// validate every document.
func knownKind(providerID string, v any) bool {
	ensure(providerID)
	regMu.RLock()
	set, ok := kinds[providerID]
	regMu.RUnlock()
	if !ok {
		return true
	}
	m, _ := v.(map[string]any)
	av, _ := m["apiVersion"].(string)
	kind, _ := m["kind"].(string)
	return set[av+" "+kind]
}

// dispatchRule is the part of one if/then entry of dispatch.json that names a kind.
type dispatchRule struct {
	If struct {
		Properties struct {
			APIVersion struct {
				Const string `json:"const"`
			} `json:"apiVersion"`
			Kind struct {
				Const string `json:"const"`
			} `json:"kind"`
		} `json:"properties"`
	} `json:"if"`
}

// RegisterTekton pins the embedded Tekton schemas for provider id. File validates each document by apiVersion
// and kind and skips a kind without a schema; it returns ErrNoSchema when no document of a file has one.
func RegisterTekton(id string, patternList []string) {
	registerLazy(id, patternList, func() { registerDispatched(id, schemas.Tekton, tektonDir, tektonBase, patternList) })
}

// registerDispatched pins the schema files in dir of fsys, and dispatch.json among them, for provider id.
func registerDispatched(id string, fsys fs.FS, dir, base string, patternList []string) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		panic("validate: " + err.Error())
	}
	var dispatch []byte
	for _, e := range entries {
		data, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			panic("validate: " + err.Error())
		}
		if e.Name() == dispatchFile {
			dispatch = data
			continue
		}
		RegisterResource(base+e.Name(), data)
	}
	var head struct {
		AllOf []dispatchRule `json:"allOf"`
	}
	if err := json.Unmarshal(dispatch, &head); err != nil || len(head.AllOf) == 0 {
		panic(fmt.Sprintf("validate: unreadable %s in %s: %v", dispatchFile, dir, err))
	}
	set := make(map[string]bool, len(head.AllOf))
	for _, r := range head.AllOf {
		p := r.If.Properties
		set[strings.Join([]string{p.APIVersion.Const, p.Kind.Const}, " ")] = true
	}
	Register(id, dispatch, patternList)
	regMu.Lock()
	kinds[id] = set
	regMu.Unlock()
}
