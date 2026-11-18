package emitkit

import (
	"bytes"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const markerPrefix = "# pipemaster:emulated "

// Marker is the comment line that tags an emulated construct so the parser can restore the feature.
func Marker(f caps.Feature, irPath string) string {
	return markerPrefix + string(f) + " " + irPath
}

// ParseMarkers returns the markers found as whole comment lines, in file order; malformed lines are skipped.
func ParseMarkers(src []byte) []ir.FeatureUse {
	var out []ir.FeatureUse
	for _, raw := range bytes.Split(src, []byte("\n")) {
		line := strings.TrimSpace(string(raw))
		rest, ok := strings.CutPrefix(line, markerPrefix)
		if !ok {
			continue
		}
		feature, path, found := strings.Cut(strings.TrimSpace(rest), " ")
		path = strings.TrimSpace(path)
		if !found || feature == "" || path == "" {
			continue
		}
		out = append(out, ir.FeatureUse{IRPath: path, Feature: caps.Feature(feature)})
	}
	return out
}
