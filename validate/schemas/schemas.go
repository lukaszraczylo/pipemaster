// Package schemas embeds the pinned provider JSON Schemas.
package schemas

import _ "embed"

// GitHubWorkflow is the GitHub Actions workflow schema.
//
//go:embed github-workflow.json
var GitHubWorkflow []byte

// GitLabCI is the GitLab CI schema.
//
//go:embed gitlab-ci.json
var GitLabCI []byte
