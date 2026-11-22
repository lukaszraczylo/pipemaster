package roundtrip_test

import (
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider"
)

const statementWorkflow = `name: ci
on: push
jobs:
  j:
    runs-on: ubuntu-latest
    outputs:
      o: ${{ steps.s.outputs.v }}
    steps:
      - id: s
        run: echo "v=1" >> "$GITHUB_OUTPUT"
  k:
    needs: j
    runs-on: ubuntu-latest
    steps:
      - run: |
          echo start
          if [ "${{ needs.j.outputs.o }}" = x ]; then
            echo y
            exit 1
          fi
          case "${{ needs.j.outputs.o }}" in
            x) echo x ;;
          esac
          echo done
`

// A script line Woodpecker, Drone or GitLab cannot translate must not leave half a compound statement behind.
func TestUntranslatableLineLeavesValidShell(t *testing.T) {
	src := fstest.MapFS{".github/workflows/ci.yml": {Data: []byte(statementWorkflow)}}
	p, _, err := prov(t, provider.GHA).Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{provider.Woodpecker, provider.Drone, provider.GitLab, provider.Argo} {
		t.Run(target, func(t *testing.T) {
			files, _, err := prov(t, target).Emit(p)
			if err != nil {
				t.Fatal(err)
			}
			checkShellSyntax(t, target, files)
		})
	}
}

const unspellableWorkflow = `name: ci
on: push
jobs:
  k:
    runs-on: ubuntu-latest
    steps:
      - run: |
          echo start
          if [ "${{ hashFiles('**/go.sum') }}" = x ]; then
            echo y
            exit 1
          fi
          echo done
`

// An expression with no spelling in any target must leave valid shell, in Tekton and Argo too.
func TestUnspellableExpressionLeavesValidShell(t *testing.T) {
	src := fstest.MapFS{".github/workflows/ci.yml": {Data: []byte(unspellableWorkflow)}}
	p, _, err := prov(t, provider.GHA).Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{provider.Woodpecker, provider.Drone, provider.GitLab, provider.Tekton, provider.Argo} {
		t.Run(target, func(t *testing.T) {
			files, rep, err := prov(t, target).Emit(p)
			if err != nil {
				t.Fatal(err)
			}
			checkShellSyntax(t, target, files)
			n := 0
			for _, e := range rep.Entries {
				if e.Feature == caps.ScriptInterpolation {
					n++
				}
			}
			if n != 1 {
				t.Errorf("want one script.interpolation entry for the commented statement, got %d", n)
			}
		})
	}
}

// A file that does not decode must fail the shell check instead of being skipped.
func TestScriptsOfReportsDecodeError(t *testing.T) {
	if _, err := scriptsOf([]byte("a: [unclosed\n")); err == nil {
		t.Fatal("want a decode error")
	}
}
