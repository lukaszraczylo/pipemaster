// Package gha parses GitHub Actions workflows, and dialect forks of them, into the IR.
package gha

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/validate"
	"github.com/lukaszraczylo/pipemaster/validate/schemas"
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
	ID          string
	DefaultHost string
	// OIDCKey is the workflow key that enables OIDC where the dialect has no id-token scope; NoOIDC says why a dialect has neither.
	OIDCKey  string
	NoOIDC   string
	Contexts []string
	Dirs     []string
	Scopes   []string
	Events   []string
	BoolKeys []string
	Ignored  []IgnoredKey
	// Caveats lists features the dialect supports natively with a caveat the report states.
	Caveats []IgnoredKey
	// SecretPrefixes and VarPrefixes start names the dialect rejects for secrets and variables; VarNames are rejected whole.
	SecretPrefixes []string
	VarPrefixes    []string
	VarNames       []string
	// DefaultCancel is true when the dialect cancels earlier push and pull_request runs while concurrency is unset.
	DefaultCancel bool
}

const (
	ghaHost     = "https://github.com"
	forgejoHost = "https://data.forgejo.org"

	oidcKey = "enable-openid-connect"
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
	Ignored:     []IgnoredKey{{caps.JobEnvironment, "Gitea ignores jobs.<id>.environment"}},
	Caveats:     []IgnoredKey{{caps.TriggerPullRequest, "the ref is refs/pull/N/head, not the merge ref"}},

	SecretPrefixes: []string{"GITHUB_", "GITEA_"},
	VarPrefixes:    []string{"GITHUB_", "GITEA_", "CI"},
	NoOIDC:         "id-token is unsupported in Gitea v28",
}

// Forgejo is Forgejo Actions.
var Forgejo = Dialect{
	ID:   provider.Forgejo,
	Dirs: []string{".forgejo/workflows", ".gitea/workflows", ".github/workflows"},
	Extra: map[NodeLevel][]string{
		NodeWorkflow: {"enable-email-notifications", oidcKey},
		NodeJob:      {oidcKey},
	},
	BoolKeys: []string{"enable-email-notifications", oidcKey},
	Scopes:   GHA.Scopes,
	Events: []string{
		"push", "issues", "issue_comment", "pull_request", "pull_request_target", "release", "schedule",
		"workflow_call", "workflow_dispatch",
	},
	Contexts:    []string{"forgejo", "forge"},
	DefaultHost: forgejoHost,
	Ignored: []IgnoredKey{
		{caps.JobEnvironment, "Forgejo does not document environment"},
		{caps.PipelinePermissions, "Forgejo does not document permissions; the token has write access except on fork pull requests"},
	},

	VarPrefixes:   []string{"FORGEJO_", "GITHUB_", "GITEA_"},
	VarNames:      []string{"CI"},
	OIDCKey:       oidcKey,
	DefaultCancel: true,
}

// Dialects indexes the registered family members by provider ID.
var Dialects = map[string]Dialect{GHA.ID: GHA, Gitea.ID: Gitea, Forgejo.ID: Forgejo}

type adapter struct{ d Dialect }

// New returns the provider for dialect d.
func New(d Dialect) provider.Provider { return &adapter{d: d} }

func init() {
	for _, d := range []Dialect{GHA, Gitea, Forgejo} {
		provider.Register(New(d))
		validate.Register(d.ID, schemas.GitHubWorkflow, []string{d.Dirs[0] + "/*.yml", d.Dirs[0] + "/*.yaml"})
		if d.ID != provider.GHA {
			validate.RegisterDialect(d.ID, validate.DialectRules{
				Workflow: d.Extra[NodeWorkflow], Job: d.Extra[NodeJob], Step: d.Extra[NodeStep], Scopes: d.Scopes, Bools: d.BoolKeys,
			})
		}
	}
}

// builtinSecrets are the job tokens a dialect provides, so no user creates them.
var builtinSecrets = []string{"GITHUB_TOKEN", "GITEA_TOKEN"}

// nameRule returns the prefix, or the whole name when exact, that makes the dialect reject a secret or variable name.
func (d Dialect) nameRule(family, name string) (rule string, exact bool) {
	var prefixes []string
	switch family {
	case expr.PrefixSecrets:
		if slices.Contains(builtinSecrets, name) {
			return "", false
		}
		prefixes = d.SecretPrefixes
	case expr.PrefixVars:
		if slices.Contains(d.VarNames, name) {
			return name, true
		}
		prefixes = d.VarPrefixes
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return p, false
		}
	}
	return "", false
}

// accepts reports whether the dialect knows the on: event; a dialect with no event list accepts every event.
func (d Dialect) accepts(event string) bool {
	return len(d.Events) == 0 || slices.Contains(d.Events, event)
}

// title is the dialect's name as the report spells it.
func (d Dialect) title() string {
	if d.ID == provider.GHA {
		return "GHA"
	}
	return strings.ToUpper(d.ID[:1]) + d.ID[1:]
}

func (a *adapter) ID() string { return a.d.ID }

// Detect reports whether the dialect's emit directory holds at least one workflow file.
func (a *adapter) Detect(fsys fs.FS) bool {
	files, err := workflowFiles(fsys, a.d.Dirs[0])
	return err == nil && len(files) > 0
}

// SourceFiles lists the workflow files of the first directory in Dirs that holds any, or none.
func (a *adapter) SourceFiles(fsys fs.FS) ([]string, error) {
	for _, d := range a.d.Dirs {
		files, err := workflowFiles(fsys, d)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || len(files) > 0 {
			return files, err
		}
	}
	return nil, nil
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
