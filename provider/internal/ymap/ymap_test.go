package ymap_test

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/provider/internal/ymap"
)

func TestAmbiguousStringsAreQuoted(t *testing.T) {
	for _, s := range []string{"on", "Off", "yes", "No", "y", "N", "true", "FALSE", "null", "~", "", "1", "010", "0x1F", "1_000", "1e3", ".5", "-3", "1:30", ".inf", "0o17", "=", "<<"} {
		m := ymap.New()
		m.SetStr("value", s)
		out, err := ymap.Marshal([]*ymap.Doc{{Root: m.N}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `value: "`+s+`"`) {
			t.Errorf("%q is not quoted: %s", s, out)
		}
		var back map[string]any
		if err := yaml.Unmarshal(out, &back); err != nil || back["value"] != s {
			t.Errorf("%q reads back as %#v (%v)", s, back["value"], err)
		}
	}
}

func TestPlainStringsStayPlain(t *testing.T) {
	for _, s := range []string{"main", "v1.2.3", "alpine:3.22", "a-b", "onward", "1.2.3", "yesterday", "nullable", "10a"} {
		m := ymap.New()
		m.SetStr("value", s)
		out, _ := ymap.Marshal([]*ymap.Doc{{Root: m.N}})
		if strings.Contains(string(out), `"`) {
			t.Errorf("%q is quoted: %s", s, out)
		}
	}
}

func TestExtStringsAreQuoted(t *testing.T) {
	n, err := ymap.PlainNode(map[string]any{"env": []any{map[string]any{"name": "A", "value": "on"}}})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := ymap.Marshal([]*ymap.Doc{{Root: n}})
	if !strings.Contains(string(out), `value: "on"`) {
		t.Errorf("the Ext value is plain: %s", out)
	}
}
