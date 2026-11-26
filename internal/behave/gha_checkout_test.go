//go:build behave

package behave

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	wfWithCheckout    = "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v7\n      - run: echo hi\n"
	wfWithoutCheckout = "jobs:\n  a:\n    steps:\n      - run: echo hi\n"
	wfForgejo         = "jobs:\n  a:\n    steps:\n      - uses: https://github.com/actions/checkout@v7\n"
	wfCall            = "jobs:\n  a:\n    uses: ./.github/workflows/child.yml\n"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestMissingCheckout(t *testing.T) {
	wf := ".github/workflows/ci.yml"
	tests := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{"checkout step", map[string]string{wf: wfWithCheckout, "data.txt": "x"}, ""},
		{"forgejo checkout step", map[string]string{wf: wfForgejo, "data.txt": "x"}, ""},
		{"no checkout, repository has files", map[string]string{wf: wfWithoutCheckout, "data.txt": "x"}, "job a has no actions/checkout step"},
		{"no checkout, nothing to read", map[string]string{wf: wfWithoutCheckout, ".pipemaster/meta.yaml": "x"}, ""},
		{"trace log is no repository file", map[string]string{wf: wfWithoutCheckout, "trace.log": "x"}, ""},
		{"reusable call has no steps", map[string]string{wf: wfCall, "data.txt": "x"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTree(t, tc.files)
			got, err := missingCheckout(dir, []string{filepath.Join(dir, wf)})
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantErr == "" && got != "" {
				t.Errorf("unexpected failure: %s", got)
			}
			if tc.wantErr != "" && !strings.Contains(got, tc.wantErr) {
				t.Errorf("got %q, want it to contain %q", got, tc.wantErr)
			}
		})
	}
}
