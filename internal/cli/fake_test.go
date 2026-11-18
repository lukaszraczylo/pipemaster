package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

// fakeProv reads "<id>.cfg" with "job <id>" and "ext <value>" lines and ignores "#" lines.
type fakeProv struct{ id string }

func init() {
	provider.Register(fakeProv{"fake"})
	provider.Register(fakeProv{"fake2"})
}

func (f fakeProv) ID() string   { return f.id }
func (f fakeProv) file() string { return f.id + ".cfg" }
func (f fakeProv) Caps() caps.Matrix {
	return caps.Matrix{caps.JobNeeds: {Level: caps.Native, Strategy: "direct"}, caps.JobCache: {Level: caps.Emulated, Strategy: "cache step", Caveat: "slow | cold"}}
}
func (f fakeProv) Detect(fsys fs.FS) bool {
	_, err := fs.Stat(fsys, f.file())
	return err == nil
}

func (f fakeProv) Parse(fsys fs.FS) (*ir.Project, *report.Report, error) {
	b, err := fs.ReadFile(fsys, f.file())
	if err != nil {
		return nil, nil, err
	}
	pl := &ir.Pipeline{ID: "main"}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "job "):
			pl.Jobs = append(pl.Jobs, &ir.Job{ID: strings.TrimPrefix(line, "job ")})
		case strings.HasPrefix(line, "ext "):
			pl.Ext = ir.Ext{f.id: strings.TrimPrefix(line, "ext ")}
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			return nil, nil, fmt.Errorf("%s: bad line %q", f.file(), line)
		}
	}
	return &ir.Project{Origin: ir.Origin{Provider: f.id, Dialect: "fake-dialect", Files: []string{f.file()}}, Pipelines: []*ir.Pipeline{pl}}, &report.Report{}, nil
}

func (f fakeProv) Emit(p *ir.Project) (map[string][]byte, *report.Report, error) {
	var b strings.Builder
	rep := &report.Report{}
	for _, pl := range p.Pipelines {
		for _, j := range pl.Jobs {
			fmt.Fprintf(&b, "job %s\n", j.ID)
			if j.ID == "emu" {
				rep.Add(report.Entry{IRPath: "main/job/emu", Feature: caps.JobCache, Level: caps.Emulated, Strategy: "cache step"})
			}
		}
		if v, ok := pl.Ext[f.id]; ok {
			fmt.Fprintf(&b, "ext %v\n", v)
		}
	}
	return map[string][]byte{f.file(): []byte(b.String())}, rep, nil
}
