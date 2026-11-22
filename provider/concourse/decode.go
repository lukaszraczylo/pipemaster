// Package concourse reads and writes Concourse pipeline files.
package concourse

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

// mergeKey is the YAML merge key; it can repeat in one mapping.
const mergeKey = "<<"

// ErrNoDocument means the input holds no YAML document.
var ErrNoDocument = errors.New("no yaml document")

// DecodeNode parses the one document of data and returns its root node. Like fly 8.0+, it rejects a repeated
// mapping key at any depth. A repeated anchor and a merge key are valid, as in fly.
func DecodeNode(data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, ErrNoDocument
		}
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("parse yaml: more than one document in a Concourse file")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if err := checkDuplicateKeys(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// Decode parses data with DecodeNode and decodes the document into v.
func Decode(data []byte, v any) error {
	doc, err := DecodeNode(data)
	if err != nil {
		return err
	}
	if err := doc.Decode(v); err != nil {
		return fmt.Errorf("decode yaml: %w", err)
	}
	return nil
}

// checkDuplicateKeys walks n and fails on the first mapping that repeats a key. yaml.v3 checks only the keys it
// stores, so a node target and a struct without the field would hide the repeat.
func checkDuplicateKeys(n *yaml.Node) error {
	switch n.Kind {
	case yaml.MappingNode:
		seen := make(map[string]int, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.ScalarNode && k.Value != mergeKey {
				if first, dup := seen[k.Value]; dup {
					return fmt.Errorf("parse yaml: line %d: mapping key %q already defined at line %d", k.Line, k.Value, first)
				}
				seen[k.Value] = k.Line
			}
			if err := checkDuplicateKeys(n.Content[i+1]); err != nil {
				return err
			}
		}
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			if err := checkDuplicateKeys(c); err != nil {
				return err
			}
		}
	}
	return nil
}
