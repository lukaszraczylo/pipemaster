package woodpecker

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/provider/internal/container"
)

const fallbackID = "workflow"

// SanitizeID makes id a workflow file name: Woodpecker names a workflow by its file name without leading dots
// and extension, so the id keeps letters, digits, underscore, dash and dot, with no leading dot.
func (adapter) SanitizeID(id string) string {
	out := container.SanitizeID(id, "_-.")
	s := strings.TrimLeft(out, ".")
	if s == "" {
		return fallbackID
	}
	return s
}
