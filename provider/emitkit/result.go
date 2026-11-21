package emitkit

import (
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
)

var resultFileRE = regexp.MustCompile(`\$\(results\.[^)\s]+\.path\)`)

// IsResultWrite reports whether a script line names the file of a Tekton result. Outside Tekton the text is a
// command substitution that fails, so no other emitter writes it.
func IsResultWrite(line string) bool { return resultFileRE.MatchString(line) }

// LoseResultWrite reports a script line that writes a Tekton result: the line is not written and the job output
// that it fed has no value in the target file.
func LoseResultWrite(lose func(path string, f caps.Feature, detail string), stepPath string) {
	lose(stepPath, caps.StepScript, "the line writes a Tekton result file, which has no form in this target; it is not written")
	jobPath := stepPath
	if i := strings.LastIndex(stepPath, "/step/"); i >= 0 {
		jobPath = stepPath[:i]
	}
	lose(jobPath, caps.JobOutputs, "a Tekton result is written by a script line, which has no form in this target; the output has no value")
}
