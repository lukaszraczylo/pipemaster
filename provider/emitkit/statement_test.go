package emitkit_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

func TestCommentSpans(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		lost  []int
		want  []emitkit.Span
	}{
		{"plain line", []string{"echo a", "echo X", "echo b"}, []int{1}, []emitkit.Span{{From: 1, To: 1}}},
		{"nothing lost", []string{"if x; then", "fi"}, nil, nil},
		{"if opener lost", []string{"echo a", "if x; then", "  echo y", "fi", "echo b"}, []int{1}, []emitkit.Span{{From: 1, To: 3}}},
		{"body line lost", []string{"if x; then", "  echo X", "fi", "echo b"}, []int{1}, []emitkit.Span{{From: 0, To: 2}}},
		{"nested if", []string{"if a; then", "  if b; then", "    echo X", "  fi", "fi", "echo z"}, []int{2}, []emitkit.Span{{From: 0, To: 4}}},
		{"elif and else", []string{"if a; then", "  echo 1", "elif b; then", "  echo X", "else", "  echo 3", "fi"}, []int{3}, []emitkit.Span{{From: 0, To: 6}}},
		{"one-liner stays one line", []string{"if x; then y; fi", "echo b"}, []int{0}, []emitkit.Span{{From: 0, To: 0}}},
		{"one-liner then multi-line", []string{"if x; then y; fi", "if X; then", "fi"}, []int{1}, []emitkit.Span{{From: 1, To: 2}}},
		{"case", []string{"case $x in", "  a) echo X ;;", "  b) echo c ;;", "esac", "echo d"}, []int{1}, []emitkit.Span{{From: 0, To: 3}}},
		{"for", []string{"for i in 1 2; do", "  echo X", "done"}, []int{1}, []emitkit.Span{{From: 0, To: 2}}},
		{"while", []string{"while true; do", "  echo X", "  break", "done"}, []int{1}, []emitkit.Span{{From: 0, To: 3}}},
		{"brace group", []string{"{", "  echo X", "}", "echo b"}, []int{1}, []emitkit.Span{{From: 0, To: 2}}},
		{"subshell", []string{"(", "  echo X", ")", "echo b"}, []int{1}, []emitkit.Span{{From: 0, To: 2}}},
		{"heredoc holding fi", []string{"cat <<EOF", "fi", "X", "EOF", "echo b"}, []int{2}, []emitkit.Span{{From: 0, To: 3}}},
		{"heredoc dash", []string{"cat <<-'EOF'", "\tfi", "\tEOF", "echo X"}, []int{3}, []emitkit.Span{{From: 3, To: 3}}},
		{"quote holding fi", []string{"echo 'a", "fi", "X'", "echo b"}, []int{0}, []emitkit.Span{{From: 0, To: 2}}},
		{"double quote holding if", []string{`echo "if x; then`, `X"`, "echo b"}, []int{1}, []emitkit.Span{{From: 0, To: 1}}},
		{"fi inside word and quote is no closer", []string{"if x; then", "  echo fi 'fi'", "  echo X", "fi"}, []int{2}, []emitkit.Span{{From: 0, To: 3}}},
		{"continuation", []string{"echo a \\", "  X", "echo b"}, []int{1}, []emitkit.Span{{From: 0, To: 1}}},
		{"pipe continuation", []string{"echo a |", "  X", "echo b"}, []int{1}, []emitkit.Span{{From: 0, To: 1}}},
		{"and continuation", []string{"make &&", "  X"}, []int{1}, []emitkit.Span{{From: 0, To: 1}}},
		{"comment holding if", []string{"# if x; then", "echo X"}, []int{1}, []emitkit.Span{{From: 1, To: 1}}},
		{"command substitution", []string{"v=$(", "  echo X", ")", "echo b"}, []int{1}, []emitkit.Span{{From: 0, To: 2}}},
		{"two statements", []string{"if X; then", "fi", "echo a", "for i in 1; do", "  X", "done"}, []int{0, 4}, []emitkit.Span{{From: 0, To: 1}, {From: 3, To: 5}}},
		{"two lost lines in one statement", []string{"if X; then", "  X", "fi"}, []int{0, 1}, []emitkit.Span{{From: 0, To: 2}}},
		{"multi-line entry", []string{"echo a\nif x; then\n  echo X", "fi"}, []int{0}, []emitkit.Span{{From: 0, To: 1}}},
		{"unclosed if runs to the end", []string{"echo a", "if x; then", "  echo X", "echo b"}, []int{2}, []emitkit.Span{{From: 1, To: 3, Open: true}}},
		{"stray fi is ignored", []string{"fi", "echo X"}, []int{1}, []emitkit.Span{{From: 1, To: 1}}},
		{"unterminated heredoc runs to the end", []string{"cat <<EOF", "X"}, []int{1}, []emitkit.Span{{From: 0, To: 1, Open: true}}},
		{"expression text is not shell", []string{"if [ \"${{ a.b }}\" = x ]; then", "fi"}, []int{0}, []emitkit.Span{{From: 0, To: 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lost := make([]bool, len(tt.lines))
			for _, i := range tt.lost {
				lost[i] = true
			}
			got := emitkit.CommentSpans(tt.lines, lost)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			// What stays must be valid shell.
			var kept []string
			for i, l := range tt.lines {
				in := false
				for _, s := range got {
					in = in || (i >= s.From && i <= s.To)
				}
				if !in {
					kept = append(kept, l)
				}
			}
			if len(got) == 0 {
				return
			}
			cmd := exec.Command("sh", "-n") // #nosec G204 -- fixed binary
			cmd.Stdin = strings.NewReader(strings.Join(kept, "\n"))
			if out, err := cmd.CombinedOutput(); err != nil && tt.name != "stray fi is ignored" {
				t.Errorf("kept lines are not valid shell: %s\n%s", out, strings.Join(kept, "\n"))
			}
		})
	}
}
