package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func prTypesTrigger(types ...any) ir.Trigger {
	return ir.Trigger{Kind: ir.TriggerPR, Ext: ir.Ext{"gha": map[string]any{"types": types}}}
}

func actionValues(t *testing.T, sensor string) string {
	t.Helper()
	i := strings.Index(sensor, "path: body.action")
	if i < 0 {
		t.Fatalf("no body.action filter:\n%s", sensor)
	}
	rest := sensor[i:]
	for _, stop := range []string{"path: body.pull_request", "exprs:"} {
		if j := strings.Index(rest[1:], stop); j > 0 {
			rest = rest[:j+1]
		}
	}
	return rest
}

func TestPullRequestTypesAreStatedExactly(t *testing.T) {
	p := triggerProject(prTypesTrigger("labeled", "closed"))
	files, rep := emit(t, p)
	got := actionValues(t, files[".argo/events/sensor.yaml"])
	for _, want := range []string{"labeled", "closed"} {
		if !strings.Contains(got, want) {
			t.Errorf("action filter lacks %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"opened", "reopened", "synchronize"} {
		if strings.Contains(got, bad) {
			t.Errorf("action filter has the default %q:\n%s", bad, got)
		}
	}
	if e := entries(rep, "(types)"); len(e) > 0 {
		t.Errorf("types are written, yet the report says they are not: %v", e)
	}
}

func TestPullRequestWithoutTypesKeepsDefaultActions(t *testing.T) {
	files, _ := emit(t, triggerProject(ir.Trigger{Kind: ir.TriggerPR}))
	got := actionValues(t, files[".argo/events/sensor.yaml"])
	for _, want := range []string{"opened", "reopened", "synchronize"} {
		if !strings.Contains(got, want) {
			t.Errorf("default action %q missing:\n%s", want, got)
		}
	}
}

func TestPullRequestTypesReadBackAndReEmit(t *testing.T) {
	files, _ := emit(t, triggerProject(prTypesTrigger("labeled", "closed")))
	back := roundTrip(t, files)
	again, _ := emit(t, back)
	got := actionValues(t, again[".argo/events/sensor.yaml"])
	if !strings.Contains(got, "labeled") || !strings.Contains(got, "closed") || strings.Contains(got, "opened") {
		t.Errorf("types lost on the Argo round trip:\n%s", got)
	}
}

func TestPullRequestTypeWithOddCharactersIsRefused(t *testing.T) {
	files, rep := emit(t, triggerProject(prTypesTrigger("labeled'] || true")))
	if s, ok := files[".argo/events/sensor.yaml"]; ok && strings.Contains(s, "true") {
		t.Errorf("an unchecked type reached the Sensor:\n%s", s)
	}
	if len(entries(rep, "type")) == 0 {
		t.Error("no report entry for the refused type")
	}
}
