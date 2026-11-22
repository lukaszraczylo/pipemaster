package validate_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/validate"
)

const nullRes = "resources: [{name: r, type: git, source: {uri: x}}]\n"

// nullProbes lists, per context, the optional keys that a pipeline can leave empty (`key:`). The template holds
// one KEY placeholder; fly decodes a null into the zero value, so the schema must take it wherever fly does.
var nullProbes = []struct {
	tmpl string
	keys string
}{
	{"resources:\n- name: r\n  type: git\n  source: {uri: x}\n  KEY:\njobs: [{name: j, plan: [{get: r}]}]\n",
		"icon tags webhook_token check_every public version old_name check_timeout expose_build_created_by source"},
	{nullRes + "jobs:\n- name: j\n  plan: [{get: r}]\n  KEY:\n",
		"serial max_in_flight public build_log_retention tags serial_groups on_success on_failure on_abort on_error ensure interruptible disable_manual_trigger disable_reruns old_name"},
	{nullRes + "jobs:\n- name: j\n  plan:\n  - get: r\n    KEY:\n",
		"trigger passed params tags version timeout attempts resource on_success ensure across fail_fast"},
	{nullRes + "jobs:\n- name: j\n  plan:\n  - put: r\n    KEY:\n",
		"params tags timeout attempts resource inputs get_params no_get"},
	{"jobs:\n- name: j\n  plan:\n  - task: t\n    config: {platform: linux, run: {path: ls}}\n    KEY:\n",
		"privileged params tags timeout input_mapping output_mapping image vars file"},
	{"jobs:\n- name: j\n  plan:\n  - task: t\n    config: {platform: linux, run: {path: ls, KEY: }}\n", "args dir user path"},
	{"jobs:\n- name: j\n  plan:\n  - task: t\n    config: {platform: linux, run: {path: ls}, KEY: }\n",
		"inputs outputs caches params container_limits image_resource rootfs_uri"},
	{"jobs:\n- name: j\n  plan:\n  - task: t\n    config: {platform: linux, run: {path: ls}, image_resource: {type: registry-image, source: {repository: a}, KEY: }}\n",
		"params version source type"},
	{"jobs:\n- name: j\n  plan:\n  - task: t\n    config: {platform: linux, run: {path: ls}, container_limits: {KEY: }}\n", "cpu memory"},
	{"jobs:\n- name: j\n  plan:\n  - task: t\n    config: {platform: linux, run: {path: ls}, inputs: [{name: a, KEY: }]}\n", "path optional name"},
	{nullRes + "jobs:\n- name: j\n  plan: [{get: r}]\n  build_log_retention: {KEY: }\n", "days builds minimum_succeeded_builds"},
	{"jobs:\n- name: j\n  plan:\n  - set_pipeline: p\n    file: a\n    KEY:\n", "file vars var_files instance_vars team"},
	{"jobs:\n- name: j\n  plan:\n  - load_var: p\n    file: a\n    KEY:\n", "format reveal"},
	{nullRes + "jobs:\n- name: j\n  plan:\n  - in_parallel: {steps: [{get: r}], KEY: }\n", "limit fail_fast"},
	{nullRes + "jobs:\n- name: j\n  plan:\n  - get: r\n    across: [{var: v, values: [1], KEY: }]\n", "max_in_flight values"},
	{nullRes + "jobs: [{name: j, plan: [{get: r}]}]\nKEY:\n", "groups resources resource_types var_sources display"},
	{"resource_types:\n- name: rt\n  type: registry-image\n  source: {repository: a}\n  KEY:\n" + nullRes + "jobs: [{name: j, plan: [{get: r}]}]\n",
		"type source tags privileged params defaults check_every"},
	{"groups:\n- name: g\n  KEY:\n  jobs: [j]\n" + nullRes + "jobs: [{name: j, plan: [{get: r}]}]\n", "resources"},
}

func TestConcourseSchemaMatchesFlyOnNull(t *testing.T) {
	for _, p := range nullProbes {
		for _, k := range strings.Fields(p.keys) {
			name := strings.TrimSpace(strings.SplitN(p.tmpl, "\n", 2)[0]) + "/" + k
			y := strings.Replace(p.tmpl, "KEY", k, 1)
			t.Run(name, func(t *testing.T) {
				schemaErr := validate.File(concourseTestID, "p.yml", []byte(y))
				out, flyErr := linttool.FlyLint(t, "p.yml", []byte(y))
				if (schemaErr == nil) != (flyErr == nil) {
					t.Fatalf("schema accepts=%v, fly accepts=%v\nschema: %v\nfly: %s\n%s", schemaErr == nil, flyErr == nil, schemaErr, out, y)
				}
			})
		}
	}
}
