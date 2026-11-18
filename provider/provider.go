// Package provider defines the contract every CI/CD dialect implements and
// the registry that finds them.
package provider

import (
	"io/fs"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Provider parses one dialect into the IR and emits the IR back out.
type Provider interface {
	ID() string
	Detect(fsys fs.FS) bool
	Parse(fsys fs.FS) (*ir.Project, *report.Report, error)
	Emit(p *ir.Project) (map[string][]byte, *report.Report, error)
	Caps() caps.Matrix
}
