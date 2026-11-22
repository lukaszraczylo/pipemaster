package roundtrip_test

import (
	"testing"
	"testing/fstest"

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
	for _, target := range []string{provider.Woodpecker, provider.Drone, provider.GitLab} {
		t.Run(target, func(t *testing.T) {
			files, _, err := prov(t, target).Emit(p)
			if err != nil {
				t.Fatal(err)
			}
			checkShellSyntax(t, target, files)
		})
	}
}
