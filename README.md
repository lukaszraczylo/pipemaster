# pipemaster

pipemaster is a Go command-line tool that converts CI/CD pipelines between nine providers through a shared intermediate representation (IR). For every feature that a pipeline uses, it reports whether the conversion is native, emulated, external or lost. Nothing is dropped silently.

Website, with live sample conversions and the full capability matrix: <https://pipemaster.raczylo.com>

Status: pre-alpha.

## What it does

- Reads the pipelines of one provider and writes the equivalent files for another provider.
- Gives each feature one verdict, and lists the verdicts in a loss report.
- Keeps the triggers of the source. A Drone pipeline with no trigger, or with only `exclude:`, and a GitLab file with no `workflow:` run on their default events. Every target gets the triggers of those events, and the report lists each event that has no form in the target.
- Adds a checkout step for a target that does not clone the repository by itself, when the source cloned it.
- Clones the other repository for a Tekton catalog `git-clone` Task. The converted pipeline never clones its own repository in its place.
- Writes GitHub Actions cache steps with a rolling key when the source key has no content hash. The scope of a GitHub cache is narrower than a GitLab cache, and the report states this.
- Converts runner labels, workspaces, volumes and artifact handoff between jobs. Tekton workspaces and Argo volumes become directories and `ws-<name>-<writer>` artifacts on targets that run each job on its own runner.
- Converts Concourse `load_var` steps to script steps, and reports a resource `check_every` as an external setting. A task image digest becomes the `version.digest` of the image resource.
- Writes the unset command in the shell of the step. In variable values, only a GitLab source keeps `$$` as an escape for one `$`.
- Does not report an `Ext` key as lost when the IR carries the key to the trip back.

## Supported providers

The provider ID is the value that you pass to `--from`, `--to`, `--via` and `--as`. Each ID links to a page with the setup outside the pipeline files, the conversion notes and the feature table.

| ID | Provider | Pipeline files |
| --- | --- | --- |
| [`gha`](docs/providers/gha.md) | GitHub Actions | `.github/workflows/*.{yml,yaml}` |
| [`gitea`](docs/providers/gitea.md) | Gitea Actions | `.gitea/workflows`, then `.github/workflows` |
| [`forgejo`](docs/providers/forgejo.md) | Forgejo Actions | `.forgejo/workflows`, then `.gitea/workflows`, then `.github/workflows` |
| [`gitlab`](docs/providers/gitlab.md) | GitLab CI | `.gitlab-ci.yml` and its local includes |
| [`woodpecker`](docs/providers/woodpecker.md) | Woodpecker CI | `.woodpecker/`, then `.woodpecker.yaml`, then `.woodpecker.yml` |
| [`drone`](docs/providers/drone.md) | Drone CI | `.drone.yml` |
| [`tekton`](docs/providers/tekton.md) | Tekton Pipelines and Triggers | `.tekton/`, including its subfolders |
| [`argo`](docs/providers/argo.md) | Argo Workflows and Argo Events | `.argo/`, including its subfolders |
| [`concourse`](docs/providers/concourse.md) | Concourse | `.concourse/`, one pipeline for each file |

The parser reads the first location of the list that exists. The pinned provider versions are in [docs/versions.md](docs/versions.md).

## Installing

With Homebrew on macOS or Linux:

```sh
brew install --cask lukaszraczylo/taps/pipemaster
```

The [GitHub releases page](https://github.com/lukaszraczylo/pipemaster/releases) has archives for Linux, macOS and Windows on amd64 and arm64. `checksums.txt` lists the SHA-256 of each archive, and `checksums.txt.sigstore.json` is its keyless cosign signature.

With Go 1.27:

```sh
go install github.com/lukaszraczylo/pipemaster/cmd/pipemaster@latest
```

To build from a checkout, run `go build -o pipemaster ./cmd/pipemaster`.

## Usage

```sh
# Convert a GitHub Actions repository to GitLab CI.
pipemaster convert ./repo --to gitlab -o ./out --report md

# Convert there and back. The command prints "no differences" when the pipeline survives.
pipemaster roundtrip ./repo --via gitlab

# Check a chain of conversions.
pipemaster roundtrip ./repo --from gha --via gitlab,gitea

# Print what a target does with each feature.
pipemaster caps --to tekton
```

| Command | Purpose |
| --- | --- |
| `detect <dir>` | List the providers whose config exists in the directory. |
| `inspect <dir> [--from P]` | Print the IR of the directory. |
| `convert <dir> --to P [--from P] [-o dir] [--report text\|md\|json] [--strict] [--embed-meta] [--force]` | Convert the pipelines and print the loss report to standard error. |
| `roundtrip <dir> --via P[,P...] [--from P]` | Convert there and back, then print any differences. |
| `caps [--from P] --to P [--format text\|md]` | Print the feature support of a target, or compare two providers. |
| `validate <dir> --as P` | Check the pipeline files against the schema of a provider. |
| `version` | Print the version. |

Exit codes: 0 success, 1 error, 2 wrong command line, 3 `--strict` and a feature is not native, 4 stale round-trip metadata (use `--force` to convert anyway).

## Reading the loss report

The report gives one verdict for each feature that the pipeline uses:

- `native`: the target has the same feature.
- `emulated`: the target gets an equivalent built from other features. A `# pipemaster:emulated` comment marks it in the converted file.
- `external`: the target must configure the feature outside the pipeline file. The report says what to set up.
- `lost`: the target cannot express the feature.

## Round trips and chains

`convert` writes `.pipemaster/meta.yaml` beside the converted files. The file holds what the target format cannot hold. With `--embed-meta`, the metadata goes into the converted file as comments instead.

If a converted file has no `.pipemaster/meta.yaml` when you read it, the file keeps the triggers that the conversion wrote.

Convert the output again, and the metadata adds one layer for each conversion. A chain through several providers can then return to the first provider exactly. If you edit a converted file and convert it again, the command exits with code 4. With `--force`, the command drops the older layers and reports each one.

Secret names map between providers. Secret values, runner registration, webhooks and branch protection do not move.

## Development

| Target | What it runs |
| --- | --- |
| `make check` | `go test -short ./...` (a deterministic sample of the chain matrix), `golangci-lint` and the Lean checks. It needs the pinned linters, so run `make linters` first. |
| `make matrix` | The full chain matrix with a 40 minute timeout. On 2026-11-27 it took 505 seconds at nine providers on a loaded 16-core machine, which is close to the 10 minute default of `go test`. |
| `make behave` | The behaviour tests. They run each converted pipeline on a real engine (act, gitlab-ci-local, Drone, Woodpecker, Tekton, Argo and Concourse) and compare its trace with the trace of the source. The run is opt-in, uses the Kiri container runtime and has a timeout of 3 hours. `make behave-tools` installs the engines. The latest run is in [testdata/behave/RESULTS.md](testdata/behave/RESULTS.md). |
| `make linters` | Builds or downloads the pinned linters into `.tools/bin`: actionlint, woodpecker-cli, drone, kubeconform, argo and fly. |
| `make lint-real` | Every test except the chain matrix, without `-short`, with every pinned linter required, then `golangci-lint`. |
| `make release-check` | `goreleaser check`, then a snapshot build of every target into `dist/` without publishing. |
| `make lean`, `make vectors` | Builds the Lean 4 model in `lean4/` and regenerates the test vectors in `testdata/lean-vectors`. |

The website lives in `docs/index.html` and reads `docs/data/`. `go test ./internal/roundtrip -run TestSiteData -update` regenerates that data from `testdata/site` and six corpus pipelines, and the test fails when the two disagree.

## Related tools

[Dagger](https://dagger.io) takes a different approach: it defines pipelines as code.

## Licence

MIT. See [LICENSE](LICENSE).
