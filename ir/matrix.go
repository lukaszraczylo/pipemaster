package ir

import "sort"

// Expand returns the GitHub Actions matrix combinations: axis product, minus
// excludes, then includes merged or appended. Combos are sorted by Key.
func Expand(m *Matrix) []Combo {
	if m == nil {
		return nil
	}
	orig := make(map[string]bool, len(m.Axes))
	for _, a := range m.Axes {
		orig[a.Name] = true
	}

	var out []Combo
	for _, c := range product(m.Axes) {
		if !excluded(c, m.Exclude) {
			out = append(out, c)
		}
	}

	base := len(out)
	for _, inc := range m.Include {
		merged := false
		for i := 0; i < base; i++ {
			if !agreesOnOriginal(out[i], inc, orig) {
				continue
			}
			out[i] = overlay(out[i], inc)
			merged = true
		}
		if !merged {
			out = append(out, overlay(nil, inc))
		}
	}
	return out
}

func product(axes []Axis) []Combo {
	if len(axes) == 0 {
		return nil
	}
	res := []Combo{{}}
	for _, a := range axes {
		next := make([]Combo, 0, len(res)*len(a.Values))
		for _, c := range res {
			for _, v := range a.Values {
				next = append(next, overlay(c, Combo{{Key: a.Name, Value: v}}))
			}
		}
		res = next
	}
	return res
}

func lookup(c Combo, key string) (string, bool) {
	for _, kv := range c {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return "", false
}

// An empty exclude entry removes nothing.
func excluded(c Combo, excl []Combo) bool {
	for _, e := range excl {
		if len(e) == 0 {
			continue
		}
		all := true
		for _, kv := range e {
			if v, ok := lookup(c, kv.Key); !ok || v != kv.Value {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func agreesOnOriginal(c, inc Combo, orig map[string]bool) bool {
	for _, kv := range inc {
		if !orig[kv.Key] {
			continue
		}
		if v, ok := lookup(c, kv.Key); ok && v != kv.Value {
			return false
		}
	}
	return true
}

// overlay copies base, applies over's pairs (later wins), and sorts by Key.
func overlay(base, over Combo) Combo {
	res := make(Combo, 0, len(base)+len(over))
	res = append(res, base...)
	for _, kv := range over {
		set := false
		for i := range res {
			if res[i].Key == kv.Key {
				res[i].Value = kv.Value
				set = true
				break
			}
		}
		if !set {
			res = append(res, kv)
		}
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].Key < res[j].Key })
	return res
}
