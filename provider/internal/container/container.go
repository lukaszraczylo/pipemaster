// Package container holds the container-step logic that does not depend on the syntax of one CI provider: image and
// entrypoint, environment and plugin settings with from_secret, services, step ID sanitising and "$" escaping.
package container

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	// Dollar is the character that provider text escapes.
	Dollar = "$"
	// FromSecret is the key of a value that reads a secret.
	FromSecret = "from_secret"

	keyImage       = "image"
	keyEntrypoint  = "entrypoint"
	keyPorts       = "ports"
	keyEnvironment = "environment"
	keyName        = "name"
	idFiller       = "-"
)

// Mapping is a decoded mapping whose keys keep their source order.
type Mapping interface {
	Keys() []string
	Get(key string) any
}

// Decoder reads the decoded values of one provider.
type Decoder interface {
	// Text returns a non-null scalar as text.
	Text(v any) (string, bool)
	// TextList reads a scalar or a list of scalars.
	TextList(v any) ([]string, bool)
	// Mapping reads a mapping.
	Mapping(v any) (Mapping, bool)
	// Template reads text that may hold references as canonical script text.
	Template(s string) (string, error)
	// Value reads text as a literal, or as an expression when it holds a reference.
	Value(s string) ir.Value
}

// Take returns the value of a key and marks it as read.
type Take func(key string) (any, bool)

// Escape writes form for each "$" of s, so the text reaches the provider's pre-processor unchanged.
func Escape(s, form string) string { return strings.ReplaceAll(s, Dollar, form) }

// Unescape reverses Escape.
func Unescape(s, form string) string { return strings.ReplaceAll(s, form, Dollar) }

// UnescapeAll reverses Escape for each item.
func UnescapeAll(l []string, form string) []string {
	out := make([]string, len(l))
	for i, s := range l {
		out[i] = Unescape(s, form)
	}
	return out
}

// SanitizeID replaces each byte of id that is not a letter, a digit or in extra with "-".
func SanitizeID(id, extra string) string {
	out := make([]byte, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !isIDByte(c) && strings.IndexByte(extra, c) < 0 {
			c = idFiller[0]
		}
		out[i] = c
	}
	return string(out)
}

func isIDByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// ReadValue reads a scalar, or a mapping with only from_secret. ok is false for any other shape.
func ReadValue(d Decoder, v any) (ir.Value, bool) {
	if t, ok := d.Text(v); ok {
		return d.Value(t), true
	}
	if m, ok := d.Mapping(v); ok && len(m.Keys()) == 1 {
		if name, ok := d.Text(m.Get(FromSecret)); ok {
			return ir.Value{Kind: ir.ValueSecret, Secret: name}, true
		}
	}
	return ir.Value{}, false
}

// Env reads the variables of an environment mapping. left lists the keys that have no Value form.
func Env(d Decoder, m Mapping) (vars []ir.Var, left []string) {
	for _, name := range m.Keys() {
		if val, ok := ReadValue(d, m.Get(name)); ok {
			vars = append(vars, ir.Var{Name: name, Value: val})
			continue
		}
		left = append(left, name)
	}
	return vars, left
}

// Settings reads the plugin settings of a mapping as With. A setting with no Value form, and every setting of a
// step that also has commands (script), is in left.
func Settings(d Decoder, m Mapping, script bool) (with map[string]ir.Value, left []string) {
	for _, name := range m.Keys() {
		if val, ok := ReadValue(d, m.Get(name)); ok && !script {
			if with == nil {
				with = map[string]ir.Value{}
			}
			with[name] = val
			continue
		}
		left = append(left, name)
	}
	return with, left
}

// ReadImage reads the image and the entrypoint. what names the owner in errors, such as "step build".
func ReadImage(d Decoder, what string, take Take) (ir.Image, error) {
	v, _ := take(keyImage)
	img, ok := d.Text(v)
	if !ok {
		return ir.Image{}, fmt.Errorf("%s: image is required", what)
	}
	ref, err := d.Template(img)
	if err != nil {
		return ir.Image{}, err
	}
	out := ir.Image{Ref: ref}
	if v, ok := take(keyEntrypoint); ok {
		l, isList := d.TextList(v)
		if !isList {
			return ir.Image{}, fmt.Errorf("%s: entrypoint must be a string or a list", what)
		}
		for _, e := range l {
			t, err := d.Template(e)
			if err != nil {
				return ir.Image{}, err
			}
			out.Entrypoint = append(out.Entrypoint, t)
		}
	}
	return out, nil
}

// Service reads one service. leftEnv lists the environment keys that have no Value form. escape is the form of
// "$" in ports.
func Service(d Decoder, name, escape string, take Take) (sv ir.Service, leftEnv []string, err error) {
	take(keyName)
	sv = ir.Service{Name: name}
	img, err := ReadImage(d, "service "+name, take)
	if err != nil {
		return sv, nil, err
	}
	sv.Image = img
	if v, ok := take(keyPorts); ok {
		l, isList := d.TextList(v)
		if !isList {
			return sv, nil, fmt.Errorf("service %s: ports must be a list", name)
		}
		sv.Ports = UnescapeAll(l, escape)
	}
	if v, ok := take(keyEnvironment); ok {
		em, isMap := d.Mapping(v)
		if !isMap {
			return sv, nil, fmt.Errorf("service %s: environment must be a mapping", name)
		}
		sv.Env, leftEnv = Env(d, em)
	}
	return sv, leftEnv, nil
}
