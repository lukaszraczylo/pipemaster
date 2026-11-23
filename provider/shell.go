package provider

// BashDefault reports whether a step with no explicit shell runs under bash on id when the image has bash: the
// GHA family always uses bash, and the GitLab runner detects it.
func BashDefault(id string) bool {
	switch Family(id) {
	case GHA, GitLab:
		return true
	}
	return false
}

// BashOrShScript runs the script in $1 under bash -e when the image has bash, else under sh -e. A caller passes
// it as the -c text of sh, with the script as a positional argument, so no quoting splices into it.
const BashOrShScript = `if command -v bash >/dev/null 2>&1; then exec bash -e -c "$1"; fi; exec sh -e -c "$1"`

// BashOrShArgv0 is $0 of the BashOrShScript wrapper.
const BashOrShArgv0 = "pipemaster"
