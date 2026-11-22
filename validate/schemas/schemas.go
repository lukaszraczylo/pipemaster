// Package schemas embeds the pinned provider JSON Schemas.
package schemas

import "embed"

// GitHubWorkflow is the GitHub Actions workflow schema.
//
//go:embed github-workflow.json
var GitHubWorkflow []byte

// GitLabCI is the GitLab CI schema.
//
//go:embed gitlab-ci.json
var GitLabCI []byte

// Woodpecker is the Woodpecker CI workflow schema.
//
//go:embed woodpecker.json
var Woodpecker []byte

// Drone is the Drone CI pipeline schema.
//
//go:embed drone.json
var Drone []byte

// KubernetesDefinitions is the Kubernetes schema file that drone.json references.
//
//go:embed kubernetes-definitions.json
var KubernetesDefinitions []byte

// Tekton holds the generated Tekton CRD schemas, the batch/v1 CronJob schema and dispatch.json, which routes a
// document to its schema by apiVersion and kind.
//
//go:embed tekton/*.json
var Tekton embed.FS

// Argo holds the Argo Workflows and Argo Events CRD schemas and dispatch.json, which routes a document to its
// schema by apiVersion and kind.
//
//go:embed argo/*.json
var Argo embed.FS

// Concourse is the Concourse pipeline schema, written by hand from the v8.3.3 Go structs.
//
//go:embed concourse.json
var Concourse []byte
