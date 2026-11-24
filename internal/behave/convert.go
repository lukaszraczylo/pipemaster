//go:build behave

package behave

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lukaszraczylo/pipemaster/report"
)

const convertTimeout = 2 * time.Minute

// Converted is the output of one pipemaster convert run.
type Converted struct {
	Report *report.Report
	Dir    string
	Stderr string
	Exit   int
}

// Convert converts the fixture to target with the repository's own CLI.
func (e *Env) Convert(ctx context.Context, fx Fixture, target string) (*Converted, error) {
	out := filepath.Join(e.Work, "conv", fx.Source, fx.Name, target)
	text, code, err := e.run(ctx, runOpts{Dir: e.Root, Timeout: convertTimeout}, e.PM,
		"convert", fx.Dir, "--from", fx.Source, "--to", target, "-o", out, "--report", "json")
	if err != nil {
		return nil, fmt.Errorf("convert %s/%s to %s: %w", fx.Source, fx.Name, target, err)
	}
	c := &Converted{Dir: out, Exit: code, Stderr: text}
	if code != 0 {
		return c, nil
	}
	i := strings.Index(text, "\n{")
	if strings.HasPrefix(text, "{") {
		i = -1
	}
	rep := &report.Report{}
	dec := json.NewDecoder(bytes.NewReader([]byte(text[i+1:])))
	if err := dec.Decode(rep); err != nil {
		return nil, fmt.Errorf("convert %s/%s to %s: parse report: %w", fx.Source, fx.Name, target, err)
	}
	c.Report = rep
	return c, nil
}
