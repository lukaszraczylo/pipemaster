package tekton

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/report"
)

// rawDoc is one YAML document of a file.
type rawDoc struct {
	top *omap
	// view, when set, is what Ext holds instead of top: a copy with the parts that the parser consumed removed.
	view any
	file string
	// head holds the comment lines above the document: the markers the emitter writes.
	head string
	kind string
	api  string
	line int
}

func (d *rawDoc) loc(line int) string {
	if line <= 0 {
		line = d.line
	}
	return fmt.Sprintf("%s:%d", d.file, line)
}

// readDocs decodes every document of the file; a null or empty document is skipped.
func readDocs(fsys fs.FS, file string, rep *report.Report) ([]*rawDoc, error) {
	data, err := fs.ReadFile(fsys, file)
	if err != nil {
		return nil, err
	}
	return readDocsData(data, file, rep)
}

// readDocsBytes reads documents that the emitter built in memory.
func readDocsBytes(data []byte) ([]*rawDoc, error) {
	return readDocsData(data, "", &report.Report{})
}

func readDocsData(data []byte, file string, rep *report.Report) ([]*rawDoc, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	var out []*rawDoc
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		b := &builder{}
		v, err := b.build(&n, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		line := n.Line
		if len(n.Content) > 0 {
			line = n.Content[0].Line
		}
		for _, l := range b.skipped {
			rep.Add(report.Entry{
				IRPath: "pipeline/", Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "dropped",
				Detail: "a merge key (<<) whose value is not a mapping adds no key and is dropped", SourceLoc: fmt.Sprintf("%s:%d", file, l),
			})
		}
		for _, rp := range b.repeats {
			rep.Add(report.Entry{
				IRPath: "pipeline/", Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "dropped",
				Detail: "key " + rp.key + " is defined twice; the last value wins and the earlier one is lost", SourceLoc: fmt.Sprintf("%s:%d", file, rp.line),
			})
		}
		if v == nil {
			continue
		}
		top, ok := asMap(v)
		if !ok {
			return nil, fmt.Errorf("%s:%d: a document must be a mapping", file, line)
		}
		kind, _ := text(top.vals["kind"])
		api, _ := text(top.vals["apiVersion"])
		out = append(out, &rawDoc{top: top, file: file, kind: kind, api: api, line: line, head: headComments(&n)})
	}
}

// headComments joins the comments above a document, wherever the YAML decoder attached them.
func headComments(n *yaml.Node) string {
	parts := []string{n.HeadComment}
	if len(n.Content) > 0 {
		root := n.Content[0]
		parts = append(parts, root.HeadComment)
		if len(root.Content) > 0 {
			parts = append(parts, root.Content[0].HeadComment)
		}
	}
	return strings.Join(parts, "\n")
}
