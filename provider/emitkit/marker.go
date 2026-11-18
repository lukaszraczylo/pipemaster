package emitkit

import (
	"bytes"
	"encoding/json"
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

// ArgPrefix starts a comment line that carries one JSON argument for the marker above it.
const ArgPrefix = "# pipemaster:arg "

// ArgLine writes v as one marker argument line. JSON keeps it on one line; the open brace of "${{" is
// escaped so a comment never holds a raw expression, and the decoder of the argument reads the escape.
func ArgLine(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return ArgPrefix + strings.ReplaceAll(string(b), "${{", `$\u007b{`), nil
}

// ParseArgs collects the argument lines that follow each marker.
func ParseArgs(src []byte) map[ir.FeatureUse][]string {
	out := map[ir.FeatureUse][]string{}
	var last []ir.FeatureUse
	for _, raw := range bytes.Split(src, []byte("\n")) {
		line := strings.TrimSpace(string(raw))
		if rest, ok := strings.CutPrefix(line, ArgPrefix); ok && len(last) == 1 {
			out[last[0]] = append(out[last[0]], rest)
			continue
		}
		last = ParseMarkers(raw)
	}
	return out
}
