package roundtrip_test

import (
	"strings"
	"testing"
)

func TestSiteCheckoutReportsNoPassthroughLoss(t *testing.T) {
	for _, id := range []string{"gitlab", "woodpecker", "drone"} {
		t.Run(id, func(t *testing.T) {
			code, _, stderr := runOut("convert", siteSample, "--to", id, "-o", t.TempDir(), "--report", "json")
			if code != 0 {
				t.Fatalf("convert = %d\n%s", code, stderr)
			}
			for _, e := range jsonReport(t, stderr) {
				if e.Feature == "ext.passthrough" && strings.Contains(e.Detail, "actions/checkout") {
					t.Errorf("checkout reported as passthrough loss: %+v", e)
				}
			}
		})
	}
}

func TestSiteSecretToGitLabIsExternal(t *testing.T) {
	code, _, stderr := runOut("convert", siteSample, "--to", "gitlab", "-o", t.TempDir(), "--report", "json")
	if code != 0 {
		t.Fatalf("convert = %d\n%s", code, stderr)
	}
	var ext int
	for _, e := range jsonReport(t, stderr) {
		if e.Feature == "step.env" && e.Level == "lost" {
			t.Errorf("step.env lost entry: %+v", e)
		}
		if e.Level == "external" && strings.Contains(e.Strategy+e.Detail, "DEPLOY_TOKEN") {
			ext++
		}
	}
	if ext != 1 {
		t.Errorf("want one external entry naming DEPLOY_TOKEN, got %d", ext)
	}
}
