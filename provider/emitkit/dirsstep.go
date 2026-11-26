package emitkit

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// DirsStepSuffix ends the ID of the step that creates the directories of a job.
const DirsStepSuffix = "-workspace-dirs"

var plainPath = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

// DirsStep returns the step that creates the directories that stand for the volumes of a source file. The source
// engine mounts them already, so the step is synthetic: ext marks it, and the emitter of the source skips it.
func DirsStep(jobID string, dirs []string, dialect, ext string) *ir.Step {
	sorted := slices.Compact(slices.Sorted(slices.Values(dirs)))
	words := make([]string, len(sorted))
	for i, d := range sorted {
		words[i] = quotePath(d)
	}
	return &ir.Step{
		ID: jobID + DirsStepSuffix, Kind: ir.StepScript, Script: []string{"mkdir -p " + strings.Join(words, " ")},
		Ext: ir.Ext{dialect: map[string]any{ext: true}},
	}
}

func quotePath(p string) string {
	if plainPath.MatchString(p) {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// UpFrom is the prefix that leads from the working directory of a step back to the checkout root, so a path
// that the IR keeps relative to the root also resolves in the step. A root, absolute or escaping directory gets none.
func UpFrom(workDir string) string {
	if workDir == "" || path.IsAbs(workDir) {
		return ""
	}
	dir := path.Clean(workDir)
	if dir == "." || dir == ".." || strings.HasPrefix(dir, "../") {
		return ""
	}
	return strings.Repeat("../", strings.Count(dir, "/")+1)
}
