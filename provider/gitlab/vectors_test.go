package gitlab_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
)

// The abstract pipeline below mirrors lean4/Pipemaster/Gitlab.lean; its JSON keys are the ones GenVectors writes.
type (
	vBlock struct {
		ID    string        `json:"id"`
		Env   []vectors.Var `json:"env"`
		Lines []string      `json:"lines"`
	}
	vJob struct {
		Needs        *[]string     `json:"needs"`
		Stage        *string       `json:"stage"`
		Retry        *int          `json:"retry"`
		Timeout      *int          `json:"timeout"`
		AllowFailure *bool         `json:"allow_failure"`
		When         *string       `json:"when"`
		Key          string        `json:"key"`
		Tags         []string      `json:"tags"`
		Variables    []vectors.Var `json:"variables"`
		Script       []vBlock      `json:"script"`
	}
	vTrig struct {
		Kind    string   `json:"kind"`
		Include []string `json:"include"`
		Idx     int      `json:"idx"`
	}
	vPipeline struct {
		Rules  *[]vTrig `json:"rules"`
		Stages []string `json:"stages"`
		Jobs   []vJob   `json:"jobs"`
		Marks  []vTrig  `json:"marks"`
	}
)

var (
	quoted     = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	unescapeRE = regexp.MustCompile(`\\(.)`)
	stepMarker = regexp.MustCompile(`(?m)^# pipemaster:step (.+)$`)
	envLine    = regexp.MustCompile(`^export ([A-Za-z_][A-Za-z0-9_]*)=(".*")$`)
)

func shellUnquote(q string) string {
	return unescapeRE.ReplaceAllString(q[1:len(q)-1], "$1")
}

// ruleTrig reads a workflow rule the emitter writes for one trigger.
func ruleTrig(text string) (vTrig, error) {
	var t vTrig
	switch {
	case strings.HasPrefix(text, "$CI_PIPELINE_SOURCE == \"merge_request_event\""):
		t.Kind = "pr"
	case strings.HasPrefix(text, "$CI_COMMIT_BRANCH"):
		t.Kind = "push"
	case strings.HasPrefix(text, "$CI_COMMIT_TAG"):
		t.Kind = "tag"
	default:
		return t, fmt.Errorf("rule %q is not modelled", text)
	}
	vals := quoted.FindAllStringSubmatch(text, -1)
	if t.Kind == "pr" && len(vals) > 0 {
		vals = vals[1:]
	}
	t.Include = []string{}
	for _, v := range vals {
		t.Include = append(t.Include, v[1])
	}
	return t, nil
}

func decodeRules(n *yaml.Node) (*[]vTrig, error) {
	es, err := vectors.Entries(n)
	if err != nil {
		return nil, err
	}
	out := []vTrig{}
	for _, e := range es {
		if e.Key != "rules" {
			return nil, fmt.Errorf("workflow key %q is not modelled", e.Key)
		}
		for i, rn := range e.Val.Content {
			res, err := vectors.Entries(rn)
			if err != nil || len(res) != 1 || res[0].Key != "if" {
				return nil, fmt.Errorf("rule %d is not a single if", i)
			}
			t, err := ruleTrig(res[0].Val.Value)
			if err != nil {
				return nil, err
			}
			t.Idx = i
			out = append(out, t)
		}
	}
	return &out, nil
}

func decodeBlocks(n *yaml.Node) ([]vBlock, error) {
	var out []vBlock
	for _, it := range n.Content {
		if m := stepMarker.FindStringSubmatch(it.HeadComment); m != nil {
			out = append(out, vBlock{ID: m[1], Lines: []string{}})
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("line %d: script item before the first step marker", it.Line)
		}
		b := &out[len(out)-1]
		if rest, ok := strings.CutPrefix(it.Value, "(\nexport "); ok && strings.HasSuffix(it.Value, "\n)\n") {
			body := strings.TrimSuffix("export "+rest, "\n)\n")
			for _, l := range strings.Split(body, "\n") {
				m := envLine.FindStringSubmatch(l)
				if m == nil || len(b.Lines) > 0 {
					b.Lines = append(b.Lines, l)
					continue
				}
				b.Env = append(b.Env, vectors.Var{Name: m[1], Value: shellUnquote(m[2])})
			}
			continue
		}
		b.Lines = append(b.Lines, it.Value)
	}
	return out, nil
}

func decodeRetry(n *yaml.Node) (int, error) {
	if n.Kind == yaml.MappingNode {
		es, err := vectors.Entries(n)
		if err != nil || len(es) != 1 || es[0].Key != "max" {
			return 0, fmt.Errorf("retry map is not modelled")
		}
		return vectors.Int(es[0].Val)
	}
	return vectors.Int(n)
}

func decodeJob(key string, n *yaml.Node) (vJob, error) {
	j := vJob{Key: key}
	es, err := vectors.Entries(n)
	if err != nil {
		return j, err
	}
	for _, e := range es {
		switch e.Key {
		case "stage":
			s := e.Val.Value
			j.Stage = &s
		case "tags":
			j.Tags, err = vectors.Strings(e.Val)
		case "needs":
			var l []string
			if l, err = vectors.Strings(e.Val); err == nil {
				j.Needs = &l
			}
		case "variables":
			j.Variables, err = vectors.Vars(e.Val)
		case "script":
			j.Script, err = decodeBlocks(e.Val)
		case "retry":
			var r int
			if r, err = decodeRetry(e.Val); err == nil {
				j.Retry = &r
			}
		case "timeout":
			var m int
			text, ok := strings.CutSuffix(e.Val.Value, " minutes")
			if m, err = strconv.Atoi(text); err == nil && ok {
				j.Timeout = &m
			} else if err == nil {
				err = fmt.Errorf("timeout %q is not in minutes", e.Val.Value)
			}
		case "allow_failure":
			var b bool
			if b, err = vectors.Bool(e.Val); err == nil {
				j.AllowFailure = &b
			}
		case "when":
			w := e.Val.Value
			j.When = &w
		default:
			err = fmt.Errorf("job key %q is not modelled", e.Key)
		}
		if err != nil {
			return j, fmt.Errorf("job %s: %w", key, err)
		}
	}
	return j, nil
}

type trigArg struct {
	Kind     string
	Branches struct{ Include []string }
	Tags     struct{ Include []string }
}

var markKinds = map[string]string{"trigger.pull_request": "pr", "trigger.push": "push", "trigger.tag": "tag"}

func decodeMarks(src []byte) ([]vTrig, error) {
	out := []vTrig{}
	for _, m := range vectors.Marks(src) {
		if m.Feature == "step.env" {
			continue
		}
		kind, ok := markKinds[m.Feature]
		rest, pok := strings.CutPrefix(m.Path, "pipeline/ci/trigger/")
		if !ok || !pok {
			return nil, fmt.Errorf("marker %s %s is not modelled", m.Feature, m.Path)
		}
		idx, err := strconv.Atoi(rest)
		if err != nil {
			return nil, err
		}
		var a trigArg
		if err := m.DecodeArg(&a); err != nil {
			return nil, err
		}
		inc := a.Branches.Include
		if kind == "tag" {
			inc = a.Tags.Include
		}
		if a.Kind != kind {
			return nil, fmt.Errorf("marker %s: kind %q", m.Path, a.Kind)
		}
		out = append(out, vTrig{Idx: idx, Kind: kind, Include: append([]string{}, inc...)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Idx < out[j].Idx })
	return out, nil
}

func decodePipeline(src []byte) (vPipeline, error) {
	var p vPipeline
	es, err := vectors.RootMapping(src)
	if err != nil {
		return p, err
	}
	for _, e := range es {
		switch e.Key {
		case "stages":
			if p.Stages, err = vectors.Strings(e.Val); err != nil {
				return p, err
			}
		case "workflow":
			if p.Rules, err = decodeRules(e.Val); err != nil {
				return p, err
			}
		default:
			j, err := decodeJob(e.Key, e.Val)
			if err != nil {
				return p, err
			}
			p.Jobs = append(p.Jobs, j)
		}
	}
	p.Marks, err = decodeMarks(src)
	return p, err
}

// TestGitlabVectors checks the emitter and the parser against the Lean model: the emitted file decodes to the
// model's abstract pipeline, and parsing it returns the model's pipeline (theorem gitlab_from_to).
func TestGitlabVectors(t *testing.T) {
	cases := vectors.Load(t, "gitlab")
	if len(cases) < vectors.MinAdapterCases {
		t.Fatalf("%d cases, want at least %d", len(cases), vectors.MinAdapterCases)
	}
	p := gitlab.New()
	for i, c := range cases {
		label := fmt.Sprintf("case %d %s", i, c.In)
		proj, err := ir.FromCore(c.In)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		files, rep, err := p.Emit(proj)
		if err != nil || len(files) != 1 {
			t.Fatalf("%s: emit: %v (%d files)", label, err, len(files))
		}
		var out struct {
			Gitlab json.RawMessage `json:"gitlab"`
			Core   json.RawMessage `json:"core"`
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		fsys := fstest.MapFS{}
		for name, data := range files {
			fsys[name] = &fstest.MapFile{Data: data}
			g, err := decodePipeline(data)
			if err != nil {
				t.Fatalf("%s: decode %s: %v\n%s", label, name, err, data)
			}
			vectors.DiffAST(t, label, out.Gitlab, g)
		}
		back, _, err := p.Parse(fsys)
		if err != nil {
			t.Fatalf("%s: parse: %v", label, err)
		}
		roundtrip.Restore(back, roundtrip.NewMeta("gha", proj, p, rep))
		core, err := ir.CoreOf(back)
		if err != nil {
			t.Fatalf("%s: parsed project: %v", label, err)
		}
		vectors.DiffCore(t, label, out.Core, core)
		if t.Failed() {
			return
		}
	}
}
