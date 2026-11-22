package ynode

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

// Doc is one YAML document of a file.
type Doc struct {
	Top *Map
	// View, when set, is what Ext holds instead of Top: a copy with the parts that the parser consumed removed.
	View any
	File string
	// Head holds the comment lines above the document: the markers the emitter writes.
	Head string
	Kind string
	API  string
	Line int
}

// Loc is the source location of a line of the document; line 0 means the document start.
func (d *Doc) Loc(line int) string {
	if line <= 0 {
		line = d.Line
	}
	return fmt.Sprintf("%s:%d", d.File, line)
}

// Ext is the form of the document that Ext holds.
func (d *Doc) Ext() any {
	if d.View != nil {
		return d.View
	}
	return Plain(d.Top)
}

// Name is the metadata name of the document, empty when it has none.
func (d *Doc) Name() string {
	md, _ := d.Top.Vals["metadata"].(*Map)
	if md == nil {
		return ""
	}
	n, _ := Text(md.Vals["name"])
	return n
}

// ReadDocs decodes every document of the file; a null or empty document is skipped.
func ReadDocs(fsys fs.FS, file string, rep *report.Report) ([]*Doc, error) {
	data, err := fs.ReadFile(fsys, file)
	if err != nil {
		return nil, err
	}
	return ReadDocsData(data, file, rep)
}

// ReadDocsData is ReadDocs for bytes already read.
func ReadDocsData(data []byte, file string, rep *report.Report) ([]*Doc, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	var out []*Doc
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		b := &Builder{}
		v, err := b.Build(&n, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		line := n.Line
		if len(n.Content) > 0 {
			line = n.Content[0].Line
		}
		for _, l := range b.Skipped {
			rep.Add(report.Entry{
				IRPath: "pipeline/", Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "dropped",
				Detail: "a merge key (<<) whose value is not a mapping adds no key and is dropped", SourceLoc: fmt.Sprintf("%s:%d", file, l),
			})
		}
		for _, rp := range b.Repeats {
			rep.Add(report.Entry{
				IRPath: "pipeline/", Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "dropped",
				Detail: "key " + rp.Key + " is defined twice; the last value wins and the earlier one is lost", SourceLoc: fmt.Sprintf("%s:%d", file, rp.Line),
			})
		}
		if sc, isNull := v.(Scal); v == nil || (isNull && sc.Null()) {
			continue
		}
		top, ok := AsMap(v)
		if !ok {
			return nil, fmt.Errorf("%s:%d: a document must be a mapping", file, line)
		}
		kind, _ := Text(top.Vals["kind"])
		api, _ := Text(top.Vals["apiVersion"])
		out = append(out, &Doc{Top: top, File: file, Kind: kind, API: api, Line: line, Head: headComments(&n)})
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
