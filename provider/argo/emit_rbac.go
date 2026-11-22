package argo

import (
	"reflect"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/report"
)

const rbacMarker = "# pipemaster:rbac"

// rbacFile is one generated RBAC document set: the account, its role and the binding.
type rbacFile struct {
	rule    map[string]any
	file    string
	account string
}

var rbacFiles = []rbacFile{
	{file: "workflow.yaml", account: workflowSA, rule: map[string]any{
		"apiGroups": []any{"argoproj.io"}, "resources": []any{"workflowtaskresults"}, "verbs": []any{"create", "patch"},
	}},
	{file: "sensor.yaml", account: sensorSA, rule: map[string]any{
		"apiGroups": []any{"argoproj.io"}, "resources": []any{"workflows"}, "verbs": []any{"create"},
	}},
}

// objects are the three documents of the file.
func (f rbacFile) objects() []map[string]any {
	meta := map[string]any{"name": f.account}
	return []map[string]any{
		{apiVersionKey: "v1", "kind": "ServiceAccount", "metadata": meta},
		{apiVersionKey: apiRBAC, "kind": "Role", "metadata": meta, "rules": []any{f.rule}},
		{apiVersionKey: apiRBAC, "kind": "RoleBinding", "metadata": meta,
			"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": f.account},
			"subjects": []any{map[string]any{"kind": "ServiceAccount", "name": f.account}}},
	}
}

var (
	rbacOnce  sync.Once
	rbacPlain []any
)

// plainRBAC is the generated documents as the parser reads them.
func plainRBAC() []any {
	rbacOnce.Do(func() {
		for _, f := range rbacFiles {
			for _, o := range f.objects() {
				n, err := plainNode(o)
				if err != nil {
					continue
				}
				docs, err := readDocsBytes(mustMarshal(n))
				if err != nil || len(docs) != 1 {
					continue
				}
				rbacPlain = append(rbacPlain, plain(docs[0].Top))
			}
		}
	})
	return rbacPlain
}

// isGeneratedRBAC is true when d carries the RBAC marker and equals a generated document. A marked document that
// differs stays in Ext, and the report says the marker was ignored.
func (p *project) isGeneratedRBAC(d *rawDoc) bool {
	if !hasLine(d.Head, rbacMarker) {
		return false
	}
	got := plain(d.Top)
	for _, w := range plainRBAC() {
		if reflect.DeepEqual(got, w) {
			return true
		}
	}
	p.rep.Add(report.Entry{
		IRPath: "pipeline/", Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "kept in Ext[" + dialect + "]", SourceLoc: d.Loc(0),
		Detail: markerIgnored + "the document carries the " + rbacMarker + " marker but differs from the generated RBAC; the document stays in Ext",
	})
	return false
}

func hasLine(head, want string) bool {
	for _, l := range strings.Split(head, "\n") {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

func mustMarshal(n *yaml.Node) []byte {
	b, err := marshalDocs([]*outDoc{{Root: n}})
	if err != nil {
		return nil
	}
	return b
}
