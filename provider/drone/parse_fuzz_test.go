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
