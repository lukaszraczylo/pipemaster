package roundtrip

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/report"
)

// HopResult is the outcome of one conversion step. Out holds the layers the output keeps. Unless Restored is
// true, the caller must append a layer built with NewMeta for the files it emits. When Restored is true, the
// caller appends a layer only if the emit into the target drops a pipeline feature that no kept layer holds.
type HopResult struct {
	Proj     *ir.Project
	Out      *meta.Stack
	Restored bool
}

// Hop applies the stored layers to proj for a conversion from source to target. Restored is true when the
// step unwinds to an older provider: the output is then a deterministic emit of the layers below. It needs a
// new layer only for what the target dialect cannot write, which the caller records. The stack applies only if
// its top layer describes the files read: it targets source, and it hashes every file in proj.Origin.Files.
// Any other stack counts as empty and the report says so.
func Hop(proj *ir.Project, in *meta.Stack, srcFS fs.FS, source, target string, force bool, rep *report.Report) (HopResult, error) {
	if in == nil || len(in.Layers) == 0 {
		return HopResult{Proj: proj, Out: &meta.Stack{}}, nil
	}
	if why := describes(in, proj.Origin.Files, source); why != "" {
		rep.Add(report.Entry{
			Feature: caps.ExtPassthrough,
			Level:   caps.Lost,
			Detail:  fmt.Sprintf("stored metadata %s, not the %s files read; not applied and is replaced", why, source),
		})
		return HopResult{Proj: proj, Out: &meta.Stack{}}, nil
	}
	if stale, err := in.Top().Stale(srcFS); err != nil {
		return staleHop(proj, in, stale, err, target, force, rep)
	}
	// The metadata names the source of these files, so the triggers an emitter made from its default events go.
	if in.Top().Source != "" {
		dropDefaultTriggers(proj)
	}
	if i := in.Find(target); i >= 0 {
		for j := len(in.Layers) - 1; j >= i; j-- {
			AddOrphans(rep, Restore(proj, in.Layers[j]))
		}
		return HopResult{Proj: proj, Out: &meta.Stack{Layers: in.Layers[:i:i]}, Restored: true}, nil
	}
	for _, m := range in.Layers {
		n := entryCount(m)
		if n == 0 {
			continue
		}
		rep.Add(report.Entry{
			Feature: caps.ExtPassthrough,
			Level:   caps.Native,
			Detail:  fmt.Sprintf("kept %d metadata %s from %s; they restore on conversion to %s", n, plural(n), m.Source, m.Source),
		})
	}
	return HopResult{Proj: proj, Out: &meta.Stack{Layers: slices.Clone(in.Layers)}}, nil
}

// describes returns why in does not describe the files read from source, or "" when it does. A chain whose
// layers do not link by provider is also refused: unwinding it would stamp hashes on a layer of another provider.
func describes(in *meta.Stack, files []string, source string) string {
	top := in.Top()
	if top.Target != "" && top.Target != source {
		return fmt.Sprintf("describes %s files", top.Target)
	}
	if missing := slices.DeleteFunc(slices.Clone(files), func(f string) bool { _, ok := top.Files[f]; return ok }); len(missing) > 0 {
		return fmt.Sprintf("does not list %s", strings.Join(missing, ", "))
	}
	for i := 1; i < len(in.Layers); i++ {
		if prev := in.Layers[i-1].Target; prev != "" && prev != in.Layers[i].Source {
			return fmt.Sprintf("holds layers that do not link (%s then %s)", prev, in.Layers[i].Source)
		}
	}
	return ""
}

func staleHop(proj *ir.Project, in *meta.Stack, stale []string, err error, target string, force bool, rep *report.Report) (HopResult, error) {
	if !errors.Is(err, meta.ErrStale) {
		return HopResult{}, fmt.Errorf("check metadata: %w", err)
	}
	if !force {
		return HopResult{}, fmt.Errorf("files changed since conversion: %w", err)
	}
	dropped := in.Layers
	if top := in.Top(); top.Source == target {
		orphans, kept := RestoreKeepingEdits(proj, top)
		AddOrphans(rep, orphans)
		AddEdited(rep, kept)
		dropped = in.Layers[:len(in.Layers)-1]
	}
	for _, m := range dropped {
		rep.Add(report.Entry{
			Feature: caps.ExtPassthrough,
			Level:   caps.Lost,
			Detail:  fmt.Sprintf("metadata from %s dropped: %s was edited (--force)", m.Source, strings.Join(stale, ", ")),
		})
	}
	return HopResult{Proj: proj, Out: &meta.Stack{}}, nil
}

func plural(n int) string {
	if n == 1 {
		return "entry"
	}
	return "entries"
}

func entryCount(m *meta.Meta) int {
	return len(m.Ext) + len(m.Residual) + len(m.Lost) + len(m.Calls)
}

// AddOrphans reports each stored path that matches no node in the converted pipeline.
func AddOrphans(rep *report.Report, orphans []string) {
	for _, path := range orphans {
		rep.Add(report.Entry{
			IRPath:  path,
			Feature: caps.ExtPassthrough,
			Level:   caps.Lost,
			Detail:  fmt.Sprintf("stored metadata for %s matches no node in the converted pipeline", path),
		})
	}
}

// AddEdited reports each node where stored metadata differed from the edited file and the file won.
func AddEdited(rep *report.Report, paths []string) {
	for _, path := range paths {
		rep.Add(report.Entry{
			IRPath:  path,
			Feature: caps.ExtPassthrough,
			Level:   caps.Lost,
			Detail:  fmt.Sprintf("%s was edited after conversion; stored metadata not applied (--force keeps the edited file)", path),
		})
	}
}
