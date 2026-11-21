package drone

import "github.com/lukaszraczylo/pipemaster/provider/internal/container"

const (
	// maxName is the longest step name Drone accepts (research drone.md 2.2, parser.go lint).
	maxName    = 100
	fallbackID = "default"
)

// SanitizeID makes id a Drone name: letters, digits, underscore, dash and dot, at most 100 bytes.
func (adapter) SanitizeID(id string) string {
	out := container.SanitizeID(id, "_-.")
	if len(out) > maxName {
		out = out[:maxName]
	}
	if out == "" {
		return fallbackID
	}
	return out
}
