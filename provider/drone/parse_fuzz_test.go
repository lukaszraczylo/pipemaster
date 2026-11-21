package drone_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/drone"
)

func FuzzParse(f *testing.F) {
	f.Add(minimal)
	f.Add("kind: pipeline\nname: a\ntrigger:\n  event: [push, pull_request]\n  branch: [main, 'release/*']\ndepends_on: [b]\nsteps:\n- name: s\n  image: x\n  commands: [echo ${DRONE_BRANCH} $$HOME]\n  when: {event: push}\n---\nkind: secret\nname: s\n...\n")
	f.Add("x: &a {image: y}\nkind: pipeline\nname: n\nservices:\n- {name: db, image: pg, ports: [1]}\nsteps:\n- name: s\n  <<: *a\n  settings: {k: [1]}\n---\nkind: signature\nhmac: x\n")
	for _, seed := range markerSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src string) {
		p, _, err := drone.New().Parse(repo(map[string]string{".drone.yml": src}))
		if err != nil {
			return
		}
		if err := ir.Validate(p); err != nil {
			t.Fatalf("Parse returned an invalid project: %v", err)
		}
		if _, err := ir.Marshal(p); err != nil {
			t.Fatalf("Marshal: %v", err)
		}
	})
}

const markerDoc = "kind: pipeline\ntype: docker\nname: test\nsteps:\n  - name: s\n    image: alpine\n    commands:\n      - echo hi\n"

// markerSeeds carry emulation markers with argument lines, including arguments that name no step or no cell.
var markerSeeds = []string{
	"# pipemaster:emulated job.matrix pipeline/ci/job/test\n# pipemaster:arg {\"matrix\":{\"Axes\":[{\"Name\":\"go\",\"Values\":[]}]},\"job\":\"test\",\"cell\":[],\"held\":[{\"script\":[\"x\"],\"pos\":99}],\"index\":0}\n" + markerDoc,
	"# pipemaster:emulated job.matrix pipeline/ci/job/test\n# pipemaster:arg {\"matrix\":{\"Axes\":[{\"Name\":\"go\",\"Values\":[\"1\",\"2\"]}]},\"job\":\"test\",\"cell\":[{\"Key\":\"go\",\"Value\":\"1\"}],\"held\":[{\"script\":[\"echo ${{ matrix.go }}\"],\"pos\":-1}],\"index\":0}\n" + markerDoc,
	"# pipemaster:emulated step.if pipeline/ci/job/test/step/s\n# pipemaster:arg {\"if\":\"ref_name == 'main'\",\"back\":\"x\",\"guard\":true}\n" + markerDoc,
	"# pipemaster:emulated job.if pipeline/ci/job/test\n# pipemaster:arg {\"if\":\"actor == 'a'\",\"back\":\"actor == 'a'\",\"guard\":true}\n" + markerDoc,
	"# pipemaster:emulated step.script_interpolation pipeline/ci/job/test\n# pipemaster:arg {\"pos\":7,\"refs\":[\"secrets.A\"]}\n" + markerDoc,
	"# pipemaster:emulated job.env pipeline/ci/job/test\n# pipemaster:arg \"- name: A\\n  value: {lit: x}\\n\"\n" + markerDoc,
	"# pipemaster:emulated step.action pipeline/ci/job/test\n# pipemaster:arg {\"at\":5,\"step\":\"id: a\\n\"}\n" + markerDoc,
}
