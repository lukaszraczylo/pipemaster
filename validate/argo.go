package validate

import "github.com/lukaszraczylo/pipemaster/validate/schemas"

const (
	argoDir  = "argo"
	argoBase = "https://pipemaster.invalid/schemas/argo/"
)

// RegisterArgo pins the embedded Argo Workflows and Argo Events schemas for provider id. File validates each
// document by apiVersion and kind and skips a kind without a schema; it returns ErrNoSchema when no document of
// a file has one.
func RegisterArgo(id string, patternList []string) {
	registerLazy(id, patternList, func() { registerDispatched(id, schemas.Argo, argoDir, argoBase, patternList) })
}
