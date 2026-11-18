package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const noLosses = "No losses: every feature converted natively."

// Render writes the report in format "text", "md" or "json".
func (r *Report) Render(w io.Writer, format string) error {
	switch format {
	case "text":
		return r.renderText(w)
	case "md":
		return r.renderMarkdown(w)
	case "json":
		return r.renderJSON(w)
	}
	return fmt.Errorf("unknown report format %q (want text, md or json)", format)
}

// sorted copies Entries so rendering never reorders the caller's slice.
func (r *Report) sorted() []Entry {
	out := make([]Entry, len(r.Entries))
	copy(out, r.Entries)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Level != b.Level {
			return a.Level > b.Level
		}
		if a.IRPath != b.IRPath {
			return a.IRPath < b.IRPath
		}
		return a.Feature < b.Feature
	})
	return out
}

func (r *Report) renderText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Loss report: %s -> %s\n", r.Source, r.Target)
	if len(r.Entries) == 0 {
		b.WriteString(noLosses + "\n")
	}
	for _, e := range r.sorted() {
		fmt.Fprintf(&b, "[%s] %s %s", e.Level, e.IRPath, e.Feature)
		if e.Strategy != "" {
			fmt.Fprintf(&b, ": %s", e.Strategy)
		}
		if e.Detail != "" {
			fmt.Fprintf(&b, " (%s)", e.Detail)
		}
		if e.SourceLoc != "" {
			fmt.Fprintf(&b, " @ %s", e.SourceLoc)
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.ReplaceAll(s, "\n", " ")
}

func (r *Report) renderMarkdown(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Loss report: %s -> %s\n\n", mdCell(r.Source), mdCell(r.Target))
	if len(r.Entries) == 0 {
		b.WriteString(noLosses + "\n")
	} else {
		b.WriteString("| Level | IR path | Feature | Strategy | Detail | Source |\n")
		b.WriteString("|---|---|---|---|---|---|\n")
		for _, e := range r.sorted() {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", e.Level,
				mdCell(e.IRPath), mdCell(string(e.Feature)), mdCell(e.Strategy),
				mdCell(e.Detail), mdCell(e.SourceLoc))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func (r *Report) renderJSON(w io.Writer) error {
	out := Report{Source: r.Source, Target: r.Target, Entries: r.sorted()}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
