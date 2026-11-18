package ir

import (
	"bytes"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// Marshal renders the project as YAML for inspect output and golden files.
func Marshal(p *Project) ([]byte, error) {
	return yaml.Marshal(p)
}

// Unmarshal parses Marshal output; unknown fields are errors.
func Unmarshal(b []byte) (*Project, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var p Project
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("decode ir yaml: %w", err)
	}
	return &p, nil
}

// jobAlias and stepAlias drop the methods so the marshalers below do not recurse.
type (
	jobAlias  Job
	stepAlias Step
)

type jobYAML struct {
	If       string `yaml:"if,omitempty"`
	jobAlias `yaml:",inline"`
}

type stepYAML struct {
	If        string `yaml:"if,omitempty"`
	stepAlias `yaml:",inline"`
}

// MarshalYAML renders If as text because Expr is an interface.
func (j Job) MarshalYAML() (any, error) {
	s, err := optExprText(j.If)
	if err != nil {
		return nil, fmt.Errorf("job %q if: %w", j.ID, err)
	}
	return jobYAML{jobAlias: jobAlias(j), If: s}, nil
}

// UnmarshalYAML parses If from text.
func (j *Job) UnmarshalYAML(n *yaml.Node) error {
	var a jobYAML
	if err := decodeKnown(n, &a); err != nil {
		return err
	}
	*j = Job(a.jobAlias)
	e, err := optParseExpr(a.If)
	if err != nil {
		return fmt.Errorf("line %d: job if: %w", n.Line, err)
	}
	j.If = e
	return nil
}

// MarshalYAML renders If as text because Expr is an interface.
func (s Step) MarshalYAML() (any, error) {
	t, err := optExprText(s.If)
	if err != nil {
		return nil, fmt.Errorf("step %q if: %w", s.ID, err)
	}
	return stepYAML{stepAlias: stepAlias(s), If: t}, nil
}

// UnmarshalYAML parses If from text.
func (s *Step) UnmarshalYAML(n *yaml.Node) error {
	var a stepYAML
	if err := decodeKnown(n, &a); err != nil {
		return err
	}
	*s = Step(a.stepAlias)
	e, err := optParseExpr(a.If)
	if err != nil {
		return fmt.Errorf("line %d: step if: %w", n.Line, err)
	}
	s.If = e
	return nil
}

// decodeKnown keeps the strict unknown-field check inside custom unmarshalers.
func decodeKnown(n *yaml.Node, out any) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(n); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	return dec.Decode(out)
}

// valueYAML has exactly one key, named after the value kind.
type valueYAML struct {
	Lit    *string `yaml:"lit,omitempty"`
	Expr   *string `yaml:"expr,omitempty"`
	Secret *string `yaml:"secret,omitempty"`
}

// MarshalYAML renders the value as a one-key map named after its kind.
func (v Value) MarshalYAML() (any, error) {
	switch v.Kind {
	case ValueLit:
		return valueYAML{Lit: &v.Lit}, nil
	case ValueSecret:
		return valueYAML{Secret: &v.Secret}, nil
	case ValueExpr:
		s, err := exprText(v.Expr)
		if err != nil {
			return nil, err
		}
		return valueYAML{Expr: &s}, nil
	}
	return nil, fmt.Errorf("invalid value kind %d", v.Kind)
}

// UnmarshalYAML parses the one-key map form.
func (v *Value) UnmarshalYAML(n *yaml.Node) error {
	var y valueYAML
	if err := decodeKnown(n, &y); err != nil {
		return fmt.Errorf("line %d: value: %w", n.Line, err)
	}
	set := 0
	for _, p := range []*string{y.Lit, y.Expr, y.Secret} {
		if p != nil {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("line %d: value needs exactly one of lit, expr, secret", n.Line)
	}
	switch {
	case y.Lit != nil:
		*v = Value{Kind: ValueLit, Lit: *y.Lit}
	case y.Secret != nil:
		*v = Value{Kind: ValueSecret, Secret: *y.Secret}
	default:
		e, err := parseExprText(*y.Expr)
		if err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		*v = Value{Kind: ValueExpr, Expr: e}
	}
	return nil
}
