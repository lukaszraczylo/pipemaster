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

// resultSink is where a neutralised result write goes. The command still runs, so a line such as
// `make && tee $(results.x.path)` keeps its other commands, and a compound command keeps its syntax.
const resultSink = "/dev/null"

// NeutralizeResultWrite replaces the result file of a script line by the null device and reports the loss: the
// job output that the line fed has no value in the target file.
func NeutralizeResultWrite(lose func(path string, f caps.Feature, detail string), stepPath, line string) string {
	return NeutralizeResultWriteWhy(lose, stepPath, line, "has no form in this target")
}

// NeutralizeResultWriteWhy is NeutralizeResultWrite with the reason, for a target that has job outputs of its own.
// why completes the sentence "a Tekton result file ...".
func NeutralizeResultWriteWhy(lose func(path string, f caps.Feature, detail string), stepPath, line, why string) string {
	if !IsResultWrite(line) {
		return line
	}
	loseResultWrite(lose, stepPath, why)
	return resultFileRE.ReplaceAllString(line, resultSink)
}

func loseResultWrite(lose func(path string, f caps.Feature, detail string), stepPath, why string) {
	lose(stepPath, caps.StepScript, "the line writes a Tekton result file, which "+why+"; the write goes to "+resultSink)
	jobPath := stepPath
	if i := strings.LastIndex(stepPath, "/step/"); i >= 0 {
		jobPath = stepPath[:i]
	}
	lose(jobPath, caps.JobOutputs, "a Tekton result is written by a script line, which "+why+"; the output has no value")
}
