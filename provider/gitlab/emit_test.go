package gitlab_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"
)

// failer is what the helpers need from testing.T and rapid.T alike.
type failer interface {
	Helper()
	Fatalf(format string, args ...any)
}

const ghaFeaturesDir = "../../testdata/gha/features"

func emitFiles(t failer, p *ir.Project) (map[string][]byte, *report.Report) {
	t.Helper()
	files, rep, err := gitlab.New().Emit(p)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return files, rep
}

func emitDoc(t testing.TB, p *ir.Project) string {
	t.Helper()
	files, _ := emitFiles(t, p)
	data, ok := files[rootFile]
	if !ok {
		t.Fatalf("no %s in %v", rootFile, reflect.ValueOf(files).MapKeys())
	}
	return string(data)
}

func asFS(files map[string][]byte) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, data := range files {
		fsys[name] = &fstest.MapFile{Data: data}
	}
	return fsys
}

func dumpFiles(files map[string][]byte) string {
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		sb.WriteString("# " + n + "\n" + string(files[n]))
	}
	return sb.String()
}

func dirNames(t testing.TB, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func gitlabFixture(t testing.TB, name string) *ir.Project {
	t.Helper()
	p, _, err := gitlab.New().Parse(os.DirFS(filepath.Join(featuresDir, name)))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return p
}

func ghaFixture(t testing.TB, name string) *ir.Project {
	t.Helper()
	p, _, err := gha.New(gha.GHA).Parse(os.DirFS(filepath.Join(ghaFeaturesDir, name)))
	if err != nil {
		t.Fatalf("parse gha %s: %v", name, err)
	}
	return p
}

func roundTrip(t failer, p *ir.Project) (*ir.Project, map[string][]byte) {
	t.Helper()
	files, _ := emitFiles(t, p)
	back, _, err := gitlab.New().Parse(asFS(files))
	if err != nil {
		t.Fatalf("reparse: %v\n%s", err, dumpFiles(files))
	}
	return back, files
}

func TestIdentity(t *testing.T) {
	for _, name := range dirNames(t, featuresDir) {
		t.Run(name, func(t *testing.T) {
			p := gitlabFixture(t, name)
			back, files := roundTrip(t, p)
			if ok, diff := ir.Equal(p, back); !ok {
				t.Fatalf("identity broken:\n%s\n--- emitted ---\n%s", diff, dumpFiles(files))
			}
			for fname, data := range files {
				if err := validate.File("gitlab", fname, data); err != nil {
					t.Fatalf("schema: %v\n%s", err, data)
				}
			}
		})
	}
}

func TestEmitValidatesSchema(t *testing.T) {
	cases := map[string]func(string) *ir.Project{
		featuresDir:    func(n string) *ir.Project { return gitlabFixture(t, n) },
		ghaFeaturesDir: func(n string) *ir.Project { return ghaFixture(t, n) },
	}
	for dir, load := range cases {
		for _, name := range dirNames(t, dir) {
			t.Run(filepath.Base(filepath.Dir(dir))+"/"+name, func(t *testing.T) {
				files, _ := emitFiles(t, load(name))
				if len(files) == 0 {
					t.Fatal("no files emitted")
				}
				for fname, data := range files {
					if fname != rootFile {
						t.Fatalf("unexpected path %s", fname)
					}
					if err := validate.File("gitlab", fname, data); err != nil {
						t.Fatalf("%v\n%s", err, data)
					}
				}
			})
		}
	}
}

func TestEmitDeterministic(t *testing.T) {
	for _, name := range dirNames(t, featuresDir) {
		p := gitlabFixture(t, name)
		a, _ := emitFiles(t, p)
		b, _ := emitFiles(t, p)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s: emit is not deterministic", name)
		}
	}
}

func TestNoRawGhaExprInOutput(t *testing.T) {
	for _, name := range dirNames(t, ghaFeaturesDir) {
		t.Run(name, func(t *testing.T) {
			files, _ := emitFiles(t, ghaFixture(t, name))
			for fname, data := range files {
				if strings.Contains(string(data), "${{") {
					t.Fatalf("%s holds a raw ${{ }} expression:\n%s", fname, data)
				}
			}
		})
	}
}

func TestScriptInterpolationMapsCanonicalRefs(t *testing.T) {
	doc := emitDoc(t, ghaFixture(t, "script.interpolation"))
	// ref_name is lost: GHA gives <n>/merge on a pull request and GitLab gives the source branch.
	for _, want := range []string{`# pipemaster:lost script.interpolation`, `echo "sha {{ sha }} on {{ ref_name }}"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q in\n%s", want, doc)
		}
	}
	files, rep := emitFiles(t, ghaFixture(t, "script.interpolation"))
	_ = files
	found := false
	for _, e := range rep.Entries {
		if e.Feature == caps.ScriptInterpolation && e.Level == caps.Lost {
			found = true
		}
	}
	if !found {
		t.Errorf("github.workspace must be a lost script.interpolation entry: %+v", rep.Entries)
	}
}

// stageIndex and needs come from the emitted YAML itself, so the test does not trust emitter bookkeeping.
func TestStagesRespectNeeds(t *testing.T) {
	loaders := map[string]func(string) *ir.Project{
		"gitlab": func(n string) *ir.Project { return gitlabFixture(t, n) },
		"gha":    func(n string) *ir.Project { return ghaFixture(t, n) },
	}
	dirs := map[string]string{"gitlab": featuresDir, "gha": ghaFeaturesDir}
	for src, load := range loaders {
		for _, name := range dirNames(t, dirs[src]) {
			t.Run(src+"/"+name, func(t *testing.T) {
				assertStagesOrder(t, emitDoc(t, load(name)), src == "gha")
			})
		}
	}
}

func assertStagesRespectNeeds(t *testing.T, doc string) {
	t.Helper()
	assertStagesOrder(t, doc, true)
}

// A GitLab-origin file may keep a job and its need in one stage; other dialects get strictly earlier stages.
func assertStagesOrder(t *testing.T, doc string, strict bool) {
	t.Helper()
	var root map[string]any
	if err := yaml.Unmarshal([]byte(doc), &root); err != nil {
		t.Fatal(err)
	}
	order := []string{".pre", "build", "test", "deploy", ".post"}
	if l, ok := root["stages"].([]any); ok {
		order = []string{".pre"}
		for _, s := range l {
			order = append(order, s.(string))
		}
		order = append(order, ".post")
	}
	idx := map[string]int{}
	for i, s := range order {
		idx[s] = i
	}
	reserved := map[string]bool{"stages": true, "variables": true, "default": true, "workflow": true, "include": true, "image": true, "services": true, "cache": true, "before_script": true, "after_script": true}
	stageOf := map[string]int{}
	for k, v := range root {
		if reserved[k] || strings.HasPrefix(k, ".") {
			continue
		}
		jm, _ := v.(map[string]any)
		st := "test"
		if s, ok := jm["stage"].(string); ok {
			st = s
		}
		i, ok := idx[st]
		if !ok && root["include"] != nil {
			// A remote template declares the stage, and it sorts before .post.
			idx[st], idx[".post"] = idx[".post"], idx[".post"]+1
			i, ok = idx[st], true
		}
		if !ok {
			t.Fatalf("job %s uses stage %q outside stages %v", k, st, order)
		}
		stageOf[k] = i
	}
	for k, v := range root {
		jm, ok := v.(map[string]any)
		if _, job := stageOf[k]; !ok || !job {
			continue
		}
		needs, _ := jm["needs"].([]any)
		for _, n := range needs {
			name := ""
			switch x := n.(type) {
			case string:
				name = x
			case map[string]any:
				name, _ = x["job"].(string)
			}
			if need, ok := stageOf[name]; ok && (need > stageOf[k] || (strict && need == stageOf[k])) {
				t.Errorf("job %s (stage %d) needs %s (stage %d)", k, stageOf[k], name, need)
			}
		}
	}
}

func TestScheduleExternal(t *testing.T) {
	p := gitlabFixture(t, "trigger.schedule")
	p.Pipelines[0].Triggers[0].Cron = "0 3 * * 1"
	doc := emitDoc(t, p)
	if !strings.Contains(doc, `$CI_PIPELINE_SOURCE == "schedule"`) {
		t.Errorf("schedule rule missing:\n%s", doc)
	}
	_, rep := emitFiles(t, p)
	found := false
	for _, e := range rep.Entries {
		if e.Feature == caps.TriggerSchedule && e.Level == caps.External &&
			strings.Contains(e.Detail, "Settings > CI/CD > Schedules") && strings.Contains(e.Detail, "0 3 * * 1") {
			found = true
		}
	}
	if !found {
		t.Errorf("want an external trigger.schedule entry with the cron and the Schedules path: %+v", rep.Entries)
	}
}

func TestEmitUnaccountedErrors(t *testing.T) {
	if _, _, err := gitlab.New().Emit(nil); err != nil {
		t.Errorf("Emit(nil) = %v, want no error", err)
	}
}

func TestCapsLedger(t *testing.T) {
	m := gitlab.New().Caps()
	for _, f := range caps.AllFeatures() {
		if _, ok := m[f]; !ok {
			t.Errorf("feature %s is not declared", f)
		}
	}
	want := map[caps.Feature]caps.Level{
		caps.TriggerSchedule: caps.External, caps.TriggerFilterPaths: caps.Native, caps.StepIf: caps.Emulated,
		caps.StepContinueOnError: caps.Emulated, caps.JobMatrix: caps.Native, caps.JobOutputs: caps.Emulated,
		caps.PipelinePermissions: caps.Lost, caps.PipelineConcurrency: caps.Emulated, caps.StepTimeout: caps.Lost, caps.StepShell: caps.Lost,
		caps.StepEnv: caps.Emulated, caps.StepWorkdir: caps.Emulated, caps.PipelineFinally: caps.Native,
		caps.JobManual: caps.Native, caps.JobRetry: caps.Native, caps.JobEnvironmentApproval: caps.External,
		caps.TriggerManual: caps.Native, caps.TriggerPullRequest: caps.Native, caps.TriggerPipelineDone: caps.Lost,
	}
	for f, lvl := range want {
		if got := m[f].Level; got != lvl {
			t.Errorf("%s = %v, want %v", f, got, lvl)
		}
	}
}

func gitlabParse(files map[string][]byte) (*ir.Project, *report.Report, error) {
	return gitlab.New().Parse(asFS(files))
}

func TestIdentityKeepsSubshellLinesInBeforeAndScript(t *testing.T) {
	src := "j:\n  before_script:\n    - (\n    - cd sub\n    - make\n    - )\n  script:\n    - (\n    - echo x\n    - )\n"
	p, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": src})
	back, files := roundTrip(t, p)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("identity broken:\n%s\n--- emitted ---\n%s", diff, dumpFiles(files))
	}
}
