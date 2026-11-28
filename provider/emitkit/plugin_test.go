package emitkit_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

func TestPluginEnv(t *testing.T) {
	lit := func(s string) ir.Value { return ir.Value{Lit: s} }
	tests := []struct {
		name    string
		with    map[string]ir.Value
		ext     ir.Ext
		want    map[string]string
		wantWhy string
	}{
		{name: "scalars are named in upper case", with: map[string]ir.Value{"build-args.x": lit("1"), "repo": lit("a/b")},
			want: map[string]string{"PLUGIN_BUILD_ARGS_X": "1", "PLUGIN_REPO": "a/b"}},
		{name: "a list of scalars is comma-joined", ext: ir.Ext{"woodpecker": map[string]any{"settings": map[string]any{"tags": []any{"latest", "v1", 2}}}},
			want: map[string]string{"PLUGIN_TAGS": "latest,v1,2"}},
		{name: "a map is JSON", ext: ir.Ext{"drone": map[string]any{"settings": map[string]any{"build_args": map[string]any{"A": "1", "B": "x"}}}},
			want: map[string]string{"PLUGIN_BUILD_ARGS": `{"A":"1","B":"x"}`}},
		{name: "a list of maps is JSON", ext: ir.Ext{"drone": map[string]any{"settings": map[string]any{"hosts": []any{map[string]any{"h": "a"}}}}},
			want: map[string]string{"PLUGIN_HOSTS": `[{"h":"a"}]`}},
		{name: "a variable in a map has no text", ext: ir.Ext{"woodpecker": map[string]any{"settings": map[string]any{"build_args": map[string]any{"A": "${CI_REPO}"}}}},
			wantWhy: "the setting build_args holds a variable or a secret inside a list or map"},
		{name: "a secret in a list has no text", ext: ir.Ext{"woodpecker": map[string]any{"settings": map[string]any{"x": []any{map[string]any{"from_secret": "S"}}}}},
			wantWhy: "the setting x holds a variable or a secret inside a list or map"},
		{name: "two settings with one name", with: map[string]ir.Value{"a-b": lit("1"), "a.b": lit("2")},
			wantWhy: "both map to PLUGIN_A_B"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars, why := emitkit.PluginEnv(&ir.Step{With: tt.with, Ext: tt.ext})
			if tt.wantWhy != "" {
				if vars != nil || !contains(why, tt.wantWhy) {
					t.Fatalf("vars = %v, why = %q, want %q", vars, why, tt.wantWhy)
				}
				return
			}
			if why != "" {
				t.Fatalf("why = %q", why)
			}
			got := map[string]string{}
			for _, v := range vars {
				got[v.Name] = v.Value.Lit
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestExtSettings(t *testing.T) {
	ext := ir.Ext{
		"drone":      map[string]any{"settings": map[string]any{"a": "1"}},
		"woodpecker": map[string]any{"settings": "not a map"},
		"gitlab":     "not a map",
	}
	if got := emitkit.ExtSettings(ext, "drone"); got["a"] != "1" {
		t.Errorf("drone settings = %v", got)
	}
	for _, d := range []string{"woodpecker", "gitlab", "absent"} {
		if got := emitkit.ExtSettings(ext, d); got != nil {
			t.Errorf("%s settings = %v, want nil", d, got)
		}
	}
	if got := emitkit.ExtSettings(nil, "drone"); got != nil {
		t.Errorf("nil ext = %v, want nil", got)
	}
}
