package emitkit

import (
	"bufio"
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
	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		rest, ok := strings.CutPrefix(line, markerPrefix)
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) != 2 {
			continue
		}
		out = append(out, ir.FeatureUse{IRPath: f[1], Feature: caps.Feature(f[0])})
	}
	return out
}
