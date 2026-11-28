package drone_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	dexpr "github.com/lukaszraczylo/pipemaster/expr/drone"
)

const changesHeredocEnd = "PIPEMASTER_CHANGES_EOF\n"

// changesScript returns the command script of the generated changes step.
func changesScript(t *testing.T) string {
	t.Helper()
	j := gjob("build", gstep("a", "echo a"))
	j.If = expr.Changes("src/**")
	out, _ := emitText(t, gproject("gitlab", j))
	for _, d := range docsOf(t, out) {
		steps, _ := d["steps"].([]any)
		for _, s := range steps {
			st := s.(map[string]any)
			if st["name"] == "pipemaster-changes" {
				cmds, _ := st["commands"].([]any)
				return strings.ReplaceAll(cmds[0].(string), "$$", "$")
			}
		}
	}
	t.Fatalf("no changes step:\n%s", out)
	return ""
}

// TestChangesScriptStaysInTheWorkspace: concurrent builds on one runner host must not share the script path.
func TestChangesScriptStaysInTheWorkspace(t *testing.T) {
	script := changesScript(t)
	if strings.Contains(script, "/tmp/") {
		t.Fatalf("the script writes to a shared path:\n%s", script)
	}
	open := strings.Index(script, "<<'PIPEMASTER_CHANGES_EOF'")
	end := strings.Index(script[open:], "\n"+changesHeredocEnd)
	if open < 0 || end < 0 {
		t.Fatalf("no heredoc in the script:\n%s", script)
	}
	head := script[:open+end+len(changesHeredocEnd)+1]
	dirs := []string{t.TempDir(), t.TempDir()}
	var wg sync.WaitGroup
	for _, d := range dirs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command("bash", "-c", head) //nolint:gosec // script text comes from the emitter under test
			cmd.Dir = d
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s: %v\n%s", d, err, out)
			}
		}()
	}
	wg.Wait()
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, dexpr.ChangesDir, "changes.sh")); err != nil {
			t.Errorf("workspace %s holds no script: %v", d, err)
		}
	}
}
