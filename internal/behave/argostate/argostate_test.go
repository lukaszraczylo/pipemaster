package argostate

import "testing"

func TestParse(t *testing.T) {
	node := func(typ, phase string) string { return `{"type":"` + typ + `","phase":"` + phase + `"}` }
	wf := func(phase string, nodes ...string) string {
		s := `{"status":{"phase":"` + phase + `","nodes":{`
		for i, n := range nodes {
			if i > 0 {
				s += ","
			}
			s += `"n` + string(rune('a'+i)) + `":` + n
		}
		return s + "}}}"
	}
	cases := []struct {
		name               string
		raw                string
		idle, done, failed bool
	}{
		{"suspended with finished pods", wf("Running", node("Suspend", "Running"), node("Pod", "Succeeded"), node("DAG", "Running")), true, false, false},
		{"suspended while a pod runs", wf("Running", node("Suspend", "Running"), node("Pod", "Running")), false, false, false},
		{"suspended while a pod waits", wf("Running", node("Suspend", "Running"), node("Pod", "Pending")), false, false, false},
		{"no suspend node", wf("Running", node("Pod", "Succeeded")), false, false, false},
		{"resumed suspend node", wf("Running", node("Suspend", "Succeeded"), node("Pod", "Succeeded")), false, false, false},
		{"finished", wf("Succeeded", node("Pod", "Succeeded")), false, true, false},
		{"failed pod beside a suspend", wf("Running", node("Suspend", "Running"), node("Pod", "Failed")), true, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, err := Parse(c.raw)
			if err != nil {
				t.Fatal(err)
			}
			if st.Idle() != c.idle || st.Done() != c.done || st.Failed != c.failed {
				t.Errorf("state %+v: idle=%v done=%v failed=%v, want %v %v %v", st, st.Idle(), st.Done(), st.Failed, c.idle, c.done, c.failed)
			}
		})
	}
	if _, err := Parse("{"); err == nil {
		t.Error("a broken document must fail loudly")
	}
}
