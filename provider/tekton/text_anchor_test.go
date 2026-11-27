package tekton

import "testing"

func TestWorkspaceLiteralLeadSet(t *testing.T) {
	cases := []struct {
		name, in string
		match    bool
	}{
		{"start", "/workspace/src/x", true},
		{"space", "ls /workspace/src", true},
		{"double quote", `"/workspace/src"`, true},
		{"single quote", `'/workspace/src'`, true},
		{"equals", "--dir=/workspace/src", true},
		{"paren", "(/workspace/src)", true},
		{"colon", "PATH=/bin:/workspace/src/bin", true},
		{"semicolon", "cd x;/workspace/src/run", true},
		{"pipe", "a|/workspace/src/run", true},
		{"ampersand", "a&/workspace/src/run", true},
		{"less", "cat </workspace/src/in", true},
		{"greater", "echo >/workspace/src/out", true},
		{"backtick", "`/workspace/src/run`", true},
		{"brace", "{/workspace/src/run;}", true},
		{"inside word", "/mnt/workspace/src/x", false},
		{"url", "https://h.example/workspace/src/y", false},
		{"letter lead", "a/workspace/src", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := workspaceLiteral.MatchString(c.in); got != c.match {
				t.Fatalf("match(%q) = %v, want %v", c.in, got, c.match)
			}
		})
	}
}
