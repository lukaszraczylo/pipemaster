// Command pipemaster converts CI/CD pipelines between providers.
package main

import (
	"os"

	"github.com/lukaszraczylo/pipemaster/internal/cli"
	_ "github.com/lukaszraczylo/pipemaster/provider/gha"
	_ "github.com/lukaszraczylo/pipemaster/provider/gitlab"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
