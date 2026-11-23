package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/lukaszraczylo/pipemaster/caps"
)

const capsUsage = "usage: pipemaster caps [--from <provider>] --to <provider> [--format text|md]\n"

func runCaps(args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("caps", flag.ContinueOnError)
	from := fset.String("from", "", "source provider")
	to := fset.String("to", "", "target provider")
	format := fset.String("format", "text", "text or md")
	if _, ok := parseArgs(fset, args, 0, capsUsage, stderr); !ok {
		return exitUsage
	}
	if *to == "" || (*format != "text" && *format != "md") {
		_, _ = fmt.Fprint(stderr, capsUsage)
		return exitUsage
	}
	q, ok := getProvider(*to, stderr)
	if !ok {
		return exitUsage
	}
	qm := q.Caps()

	var rows [][]string
	if *from == "" {
		rows = [][]string{{"FEATURE", strings.ToUpper(q.ID()), "STRATEGY"}}
		for _, f := range caps.AllFeatures() {
			qs := qm.Lookup(f)
			rows = append(rows, []string{string(f), qs.Level.String(), capsStrategy(qs)})
		}
	} else {
		p, ok := getProvider(*from, stderr)
		if !ok {
			return exitUsage
		}
		pm := p.Caps()
		rows = [][]string{{"FEATURE", strings.ToUpper(p.ID()), strings.ToUpper(q.ID()), "STRATEGY"}}
		for _, f := range caps.AllFeatures() {
			qs := qm.Lookup(f)
			rows = append(rows, []string{string(f), pm.Lookup(f).Level.String(), qs.Level.String(), capsStrategy(qs)})
		}
	}

	if *format == "md" {
		writeCapsMarkdown(stdout, rows)
		return exitOK
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		_, _ = fmt.Fprintln(tw, strings.TrimRight(strings.Join(r, "\t"), " \t"))
	}
	if err := tw.Flush(); err != nil {
		_, _ = fmt.Fprintf(stderr, "write table: %v\n", err)
		return exitError
	}
	return exitOK
}

func writeCapsMarkdown(w io.Writer, rows [][]string) {
	for i, r := range rows {
		cells := make([]string, len(r))
		for j, c := range r {
			cells[j] = strings.ReplaceAll(c, "|", `\|`)
			if i == 0 {
				cells[j] = strings.ToUpper(cells[j][:1]) + strings.ToLower(cells[j][1:])
			}
		}
		_, _ = fmt.Fprintf(w, "| %s |\n", strings.Join(cells, " | "))
		if i == 0 {
			_, _ = fmt.Fprintf(w, "|%s\n", strings.Repeat(" --- |", len(r)))
		}
	}
}

func capsStrategy(s caps.Support) string {
	if s.Caveat == "" {
		return s.Strategy
	}
	return strings.TrimSpace(s.Strategy + " (" + s.Caveat + ")")
}
