package argo_test

import (
	"strings"
	"testing"
)

func TestQuotedHelmTemplateIsNamedInTheReport(t *testing.T) {
	src := `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: "{{ .Release.Name }}-ci"
spec:
  entrypoint: main
  templates:
    - name: main
      container:
        image: "{{ .Values.image }}"
        command: [sh, -c]
        args: ["true"]
`
	p, rep, err := parseRepo(map[string]string{".argo/chart/wf.yaml": src, ".argo/ok.yaml": minimalWorkflow})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntry(rep, ".argo/chart/wf.yaml") || !hasEntry(rep, "Helm") {
		t.Errorf("no entry names the Helm file: %+v", rep.Entries)
	}
	for _, pl := range p.Pipelines {
		if strings.Contains(pl.ID, "{{") {
			t.Errorf("the pipeline id carries a Helm tag: %q", pl.ID)
		}
	}
}

const minimalWorkflow = `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: ok
spec:
  entrypoint: main
  templates:
    - name: main
      container:
        image: alpine
        command: [sh, -c]
        args: ["true"]
`
