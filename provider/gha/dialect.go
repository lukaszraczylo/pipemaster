// Package gha parses GitHub Actions workflows, and dialect forks of them, into the IR.
package gha

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/validate"
)

// NodeLevel names the workflow node that a dialect-only key sits on.
type NodeLevel int

// The node levels a Dialect.Extra key can sit on.
const (
	NodeWorkflow NodeLevel = iota
	NodeJob
	NodeStep
)

// IgnoredKey is a modelled key that a dialect accepts but does not enforce.
type IgnoredKey struct {
	Feature caps.Feature
	Caveat  string
}

// Dialect is the delta table that distinguishes one GitHub Actions family member from another.
// ID names the provider; Dirs[0] is the emit directory and the others are read-only fallbacks.
type Dialect struct {
	Extra       map[NodeLevel][]string
	Contexts    []string
	ID          string
	DefaultHost string
	Dirs        []string
	Scopes      []string
	Events      []string
	BoolKeys    []string
	Ignored     []IgnoredKey
}

const (
	ghaHost     = "https://github.com"
	forgejoHost = "https://data.forgejo.org"
)

// GHA is GitHub Actions itself, the base dialect.
var GHA = Dialect{
	ID:          provider.GHA,
	Dirs:        []string{".github/workflows"},
	Scopes:      validate.PermissionScopes(),
	DefaultHost: ghaHost,
}

// Gitea is Gitea Actions.
var Gitea = Dialect{
	ID:   provider.Gitea,
	Dirs: []string{".gitea/workflows", ".github/workflows"},
	Scopes: []string{
		"actions", "contents", "issues", "packages", "pull-requests", "code", "releases", "wiki", "projects",
	},
	Events: []string{
		"create", "delete", "fork", "gollum", "push", "schedule", "workflow_dispatch", "workflow_call",
		"issues", "issue_comment", "pull_request", "pull_request_target", "pull_request_review",
		"pull_request_review_comment", "release", "registry_package", "workflow_run",
	},
	Contexts:    []string{"gitea"},
	DefaultHost: ghaHost,
}

// Forgejo is Forgejo Actions.
var Forgejo = Dialect{
	ID:   provider.Forgejo,
	Dirs: []string{".forgejo/workflows", ".gitea/workflows", ".github/workflows"},
	Extra: map[NodeLevel][]string{
		NodeWorkflow: {"enable-email-notifications", "enable-openid-connect"},
		NodeJob:      {"enable-openid-connect"},
	},
	BoolKeys: []string{"enable-email-notifications", "enable-openid-connect"},
	Scopes:   GHA.Scopes,
	Events: []string{
		"push", "issues", "issue_comment", "pull_request", "pull_request_target", "release", "schedule",
		"workflow_call", "workflow_dispatch",
	},
	Contexts:    []string{"forgejo", "forge"},
	DefaultHost: forgejoHost,
}

// Dialects indexes the registered family members by provider ID.
var Dialects = map[string]Dialect{GHA.ID: GHA, Gitea.ID: Gitea, Forgejo.ID: Forgejo}

type adapter struct{ d Dialect }

// New returns the provider for dialect d.
func New(d Dialect) provider.Provider { return &adapter{d: d} }

func init() {
	for _, d := range []Dialect{GHA, Gitea, Forgejo} {
		provider.Register(New(d))
		if d.ID != provider.GHA {
			validate.RegisterDialect(d.ID, validate.DialectRules{
				Workflow: d.Extra[NodeWorkflow], Job: d.Extra[NodeJob], Step: d.Extra[NodeStep], Scopes: d.Scopes, Bools: d.BoolKeys,
			})
		}
	}
}

func (a *adapter) ID() string { return a.d.ID }

// Detect reports whether the dialect's emit directory holds at least one workflow file.
func (a *adapter) Detect(fsys fs.FS) bool {
	files, err := workflowFiles(fsys, a.d.Dirs[0])
	return err == nil && len(files) > 0
}

// readDir lists the workflow files of the first directory in Dirs that exists, with no merge.
func (a *adapter) readDir(fsys fs.FS) ([]string, error) {
	for _, d := range a.d.Dirs {
		if _, err := fs.Stat(fsys, d); err != nil {
			continue
		}
		files, err := workflowFiles(fsys, d)
		if err != nil {
			return nil, fmt.Errorf("%s: read %s: %w", a.d.ID, d, err)
		}
		if len(files) > 0 {
			return files, nil
		}
		return nil, fmt.Errorf("%s: no workflow files in %s", a.d.ID, d)
	}
	return nil, fmt.Errorf("%s: no workflow files in %s", a.d.ID, strings.Join(a.d.Dirs, ", "))
}

// workflowFiles lists the .yml and .yaml files directly inside dir, sorted by name.
func workflowFiles(fsys fs.FS, dir string) ([]string, error) {
	ents, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		ext := strings.ToLower(path.Ext(e.Name()))
		if !e.IsDir() && (ext == ".yml" || ext == ".yaml") {
			out = append(out, path.Join(dir, e.Name()))
		}
	}
	return out, nil
}
