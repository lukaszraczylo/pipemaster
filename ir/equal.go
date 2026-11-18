package ir

import (
	"reflect"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// Equal reports whether two projects are semantically the same after normalisation and returns a diff when not.
// Origin is provenance and is ignored. Inputs are not modified; nil and empty collections compare equal.
func Equal(a, b *Project) (bool, string) {
	ca, cb := clone(a), clone(b)
	if ca != nil {
		ca.Origin = Origin{}
	}
	if cb != nil {
		cb.Origin = Origin{}
	}
	Normalize(ca)
	Normalize(cb)
	d := cmp.Diff(ca, cb, cmpopts.EquateEmpty())
	return d == "", d
}

func clone(p *Project) *Project {
	if p == nil {
		return nil
	}
	return deepCopy(reflect.ValueOf(p)).Interface().(*Project)
}

// deepCopy copies pointers, slices, maps and interfaces so Normalize cannot touch the caller's data.
func deepCopy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		n := reflect.New(v.Type().Elem())
		n.Elem().Set(deepCopy(v.Elem()))
		return n
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		n := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			n.Index(i).Set(deepCopy(v.Index(i)))
		}
		return n
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		n := reflect.MakeMapWithSize(v.Type(), v.Len())
		for _, k := range v.MapKeys() {
			n.SetMapIndex(deepCopy(k), deepCopy(v.MapIndex(k)))
		}
		return n
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		n := reflect.New(v.Type()).Elem()
		inner := v.Elem()
		if e, ok := inner.Interface().(Ext); ok {
			// yaml decodes nested maps under Ext as Ext; parsers store map[string]any.
			inner = reflect.ValueOf(map[string]any(e))
		}
		n.Set(deepCopy(inner))
		return n
	case reflect.Struct:
		n := reflect.New(v.Type()).Elem()
		n.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if n.Field(i).CanSet() {
				n.Field(i).Set(deepCopy(v.Field(i)))
			}
		}
		return n
	default:
		return v
	}
}
