package argo_test

import (
	"strings"
	"testing"
)

func TestSensorPullRequestWithoutActionFilterReportsTheDefaultActions(t *testing.T) {
	_, entries := triggersOf(t, map[string]string{"es.yaml": eventSource, "s.yaml": sensor(dep("      - {path: headers.X-Github-Event, type: string, value: [\"pull_request\"]}\n"), runCI)})
	if !strings.Contains(entries, "pull request actions") {
		t.Fatalf("no entry for the narrowing: %s", entries)
	}
}
