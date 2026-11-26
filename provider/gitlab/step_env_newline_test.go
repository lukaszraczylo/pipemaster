package gitlab_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// A step variable with newlines goes into the script as one export whose quotes span the lines, and comes back as it went.
func TestStepEnvWithNewlinesIsExportedAndRestored(t *testing.T) {
	for _, val := range []string{"src\ndocs", "a\n\"b\"\n$X\\n", "one\n\ntwo\n"} {
		mk := glStep("make")
		mk.Env = []ir.Var{{Name: "A", Value: ir.Value{Lit: val}}, {Name: "B", Value: ir.Value{Lit: "x"}}}
		out, back, rep := emitParse(t, glProject("gitlab", bkProducer(mk), bkConsumer("echo ${{ needs.prod.outputs.v }}", false)), nil)
		for _, e := range rep.Entries {
			if e.Feature == caps.StepEnv && e.Level == caps.Lost {
				t.Errorf("%q: env lost: %s", val, e.Detail)
			}
		}
		env := jobByID(t, back, "prod").Steps[0].Env
		if len(env) != 2 || env[0].Value.Lit != val || env[1].Value.Lit != "x" {
			t.Errorf("%q: env after the trip = %+v\n%s", val, env, out)
		}
	}
}
