// Command pipemaster converts CI/CD pipelines between providers.
package main

import (
	"os"

	"github.com/lukaszraczylo/pipemaster/internal/cli"
	_ "github.com/lukaszraczylo/pipemaster/provider/drone"
	_ "github.com/lukaszraczylo/pipemaster/provider/gha"
	_ "github.com/lukaszraczylo/pipemaster/provider/gitlab"
	_ "github.com/lukaszraczylo/pipemaster/provider/tekton"
	_ "github.com/lukaszraczylo/pipemaster/provider/woodpecker"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
