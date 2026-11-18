package gha

import "github.com/lukaszraczylo/pipemaster/caps"

// Caps declares nothing until the emitter lands; undeclared features read as lost.
func (a *adapter) Caps() caps.Matrix { return caps.Matrix{} }
