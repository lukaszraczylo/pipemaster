package ir

import (
	"reflect"
	"sort"
	"testing"

	"pgregory.net/rapid"
)

// Semantics checked against this page on 2026-11-18; the page states no
// exclude-before-include order, which follows GitHub's workflow-syntax text.
const matrixDocURL = "https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/run-job-variations"

func c(kv ...string) Combo {
	var out Combo
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, KV{Key: kv[i], Value: kv[i+1]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func axes2x3() []Axis {
	return []Axis{{Name: "os", Values: []string{"a", "b"}}, {Name: "v", Values: []string{"1", "2", "3"}}}
}

func TestExpandProduct(t *testing.T) {
	got := Expand(&Matrix{Axes: axes2x3()})
	want := []Combo{c("os", "a", "v", "1"), c("os", "a", "v", "2"), c("os", "a", "v", "3"),
		c("os", "b", "v", "1"), c("os", "b", "v", "2"), c("os", "b", "v", "3")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestExpandNilAndEmpty(t *testing.T) {
	if got := Expand(nil); got != nil {
		t.Fatalf("nil matrix: got %v", got)
	}
	if got := Expand(&Matrix{}); len(got) != 0 {
		t.Fatalf("empty matrix: got %v", got)
	}
}

func TestExpandExcludePartialMatch(t *testing.T) {
	got := Expand(&Matrix{Axes: axes2x3(), Exclude: []Combo{c("os", "a")}})
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	for _, x := range got {
		if x[0].Value != "b" {
			t.Fatalf("os=a survived: %v", x)
		}
	}
}

func TestExpandIncludeExtends(t *testing.T) {
	got := Expand(&Matrix{Axes: axes2x3(), Include: []Combo{c("os", "a", "extra", "x")}})
	if len(got) != 6 {
		t.Fatalf("count %d", len(got))
	}
	for _, x := range got {
		_, has := lookup(x, "extra")
		if v, _ := lookup(x, "os"); has != (v == "a") {
			t.Fatalf("extra on wrong combo: %v", x)
		}
	}
}

func TestExpandIncludeAppends(t *testing.T) {
	got := Expand(&Matrix{Axes: axes2x3(), Include: []Combo{c("os", "z", "v", "9")}})
	if len(got) != 7 || !reflect.DeepEqual(got[6], c("os", "z", "v", "9")) {
		t.Fatalf("got %v", got)
	}
}

func TestExpandIncludeNeverOverwritesOriginal(t *testing.T) {
	got := Expand(&Matrix{Axes: []Axis{{Name: "os", Values: []string{"a"}}}, Include: []Combo{c("os", "b")}})
	if len(got) != 2 || !reflect.DeepEqual(got[0], c("os", "a")) {
		t.Fatalf("got %v", got)
	}
}

func TestExpandLaterIncludeOverwritesEarlierAdded(t *testing.T) {
	m := &Matrix{
		Axes:    []Axis{{Name: "fruit", Values: []string{"apple"}}},
		Include: []Combo{c("color", "green"), c("color", "pink", "fruit", "apple")},
	}
	got := Expand(m)
	if len(got) != 1 || !reflect.DeepEqual(got[0], c("color", "pink", "fruit", "apple")) {
		t.Fatalf("got %v", got)
	}
}

func TestExpandExcludeBeforeInclude(t *testing.T) {
	m := &Matrix{Axes: axes2x3(), Exclude: []Combo{c("os", "a")}, Include: []Combo{c("os", "a", "extra", "x")}}
	got := Expand(m)
	if len(got) != 4 || !reflect.DeepEqual(got[3], c("extra", "x", "os", "a")) {
		t.Fatalf("got %v", got)
	}
}

func TestExpandIncludeOnly(t *testing.T) {
	got := Expand(&Matrix{Include: []Combo{c("a", "1"), c("b", "2")}})
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestExpandKeysSorted(t *testing.T) {
	m := &Matrix{Axes: []Axis{{Name: "z", Values: []string{"1"}}, {Name: "a", Values: []string{"1"}}}, Include: []Combo{c("m", "1")}}
	for _, x := range Expand(m) {
		if !sort.SliceIsSorted(x, func(i, j int) bool { return x[i].Key < x[j].Key }) {
			t.Fatalf("unsorted %v", x)
		}
	}
}

func refKey(m map[string]string) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	out := ""
	for _, k := range ks {
		out += k + "=" + m[k] + ";"
	}
	return out
}

func toMap(cb Combo) map[string]string {
	m := map[string]string{}
	for _, kv := range cb {
		m[kv.Key] = kv.Value
	}
	return m
}

// refExpand is an independent model of the GitHub rules; it shares no code with Expand.
func refExpand(m *Matrix) (rem, out []map[string]string) {
	prod := []map[string]string{{}}
	for _, a := range m.Axes {
		var next []map[string]string
		for _, p := range prod {
			for _, v := range a.Values {
				q := map[string]string{a.Name: v}
				for k, x := range p {
					q[k] = x
				}
				next = append(next, q)
			}
		}
		prod = next
	}
	if len(m.Axes) == 0 {
		prod = nil
	}
	isAxis := map[string]bool{}
	for _, a := range m.Axes {
		isAxis[a.Name] = true
	}
	for _, p := range prod {
		drop := false
		for _, e := range m.Exclude {
			if len(e) == 0 {
				continue
			}
			hit := true
			for _, kv := range e {
				if v, ok := p[kv.Key]; !ok || v != kv.Value {
					hit = false
				}
			}
			drop = drop || hit
		}
		if !drop {
			rem = append(rem, p)
		}
	}
	for _, p := range rem {
		cp := map[string]string{}
		for k, v := range p {
			cp[k] = v
		}
		out = append(out, cp)
	}
	var extra []map[string]string
	for _, inc := range m.Include {
		hit := false
		for i, p := range rem {
			ok := true
			for _, kv := range inc {
				if isAxis[kv.Key] && p[kv.Key] != kv.Value {
					ok = false
				}
			}
			if ok {
				hit = true
				for _, kv := range inc {
					out[i][kv.Key] = kv.Value
				}
			}
		}
		if !hit {
			e := map[string]string{}
			for _, kv := range inc {
				e[kv.Key] = kv.Value
			}
			extra = append(extra, e)
		}
	}
	return rem, append(out, extra...)
}

func TestExpandMatchesReference(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		na := rapid.IntRange(0, 3).Draw(t, "na")
		var axes []Axis
		for i := 0; i < na; i++ {
			vals := rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"1", "2", "3"}), 1, 3, rapid.ID[string]).Draw(t, "vals")
			axes = append(axes, Axis{Name: string(rune('a' + i)), Values: vals})
		}
		key := rapid.SampledFrom([]string{"a", "b", "c", "x"})
		val := rapid.SampledFrom([]string{"1", "2", "3", "9"})
		entry := rapid.Custom(func(t *rapid.T) Combo {
			ks := rapid.SliceOfNDistinct(key, 1, 3, rapid.ID[string]).Draw(t, "ks")
			var cb Combo
			for _, k := range ks {
				cb = append(cb, KV{Key: k, Value: val.Draw(t, "v")})
			}
			sort.Slice(cb, func(i, j int) bool { return cb[i].Key < cb[j].Key })
			return cb
		})
		m := &Matrix{Axes: axes, Include: rapid.SliceOfN(entry, 0, 3).Draw(t, "inc"), Exclude: rapid.SliceOfN(entry, 0, 3).Draw(t, "exc")}

		got := Expand(m)
		rem, want := refExpand(m)

		var gk, wk []string
		for _, g := range got {
			gk = append(gk, refKey(toMap(g)))
		}
		for _, w := range want {
			wk = append(wk, refKey(w))
		}
		sort.Strings(gk)
		sort.Strings(wk)
		if !reflect.DeepEqual(gk, wk) {
			t.Fatalf("multiset mismatch for %+v:\n got %v\nwant %v", m, gk, wk)
		}

		isAxis := map[string]bool{}
		for _, a := range axes {
			isAxis[a.Name] = true
		}
		// (a) product combos keep their original axis values; output prefix follows product order.
		if len(got) < len(rem) {
			t.Fatalf("fewer combos than product remainder: %d < %d", len(got), len(rem))
		}
		for i, p := range rem {
			g := toMap(got[i])
			for k, v := range p {
				if g[k] != v {
					t.Fatalf("axis %s overwritten in combo %d: %v vs %v", k, i, g, p)
				}
			}
		}
		// (b) an include's pairs survive unless a later include shares a key and may overwrite them.
		for i, inc := range m.Include {
			shared := false
			for _, later := range m.Include[i+1:] {
				for _, a := range inc {
					for _, b := range later {
						shared = shared || a.Key == b.Key
					}
				}
			}
			if shared {
				continue
			}
			found := false
			for _, g := range got {
				gm := toMap(g)
				all := true
				for _, kv := range inc {
					all = all && gm[kv.Key] == kv.Value
				}
				found = found || all
			}
			if !found {
				t.Fatalf("include %v absent from output %v", inc, got)
			}
		}
	})
}
