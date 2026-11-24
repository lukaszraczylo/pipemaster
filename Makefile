.PHONY: test chains lint lint-real lean vectors check linters release-check behave behave-tools

test:
	go test ./...

# The full chain matrix; `make check` runs a deterministic sample of it.
chains: export PIPEMASTER_REQUIRE_LINTERS = 1
chains: export PATH := $(CURDIR)/.tools/bin:$(PATH)
chains:
	go test ./internal/roundtrip -run TestChainMatrix -count=1

lint:
	golangci-lint run

# Every test that runs a real linter, with every pinned linter required. The chain matrix runs under `make chains`.
lint-real: export PIPEMASTER_REQUIRE_LINTERS = 1
lint-real: export PATH := $(CURDIR)/.tools/bin:$(PATH)
lint-real: linters
	go test -count=1 -skip TestChainMatrix ./...
	golangci-lint run

linters:
	scripts/install-linters.sh

lean:
	cd lean4 && lake build

vectors:
	cd lean4 && lake exe gen-vectors ../testdata/lean-vectors

# A missing actionlint fails the tests here instead of skipping them.
check: export PIPEMASTER_REQUIRE_LINTERS = 1
check:
	go test -short ./...
	golangci-lint run
	scripts/check-vectors.sh
	scripts/lean-axioms.sh
	scripts/lean-currency.sh

# Validates the release config and builds every target into dist/ without publishing.
release-check:
	goreleaser check
	goreleaser release --snapshot --clean --skip=publish

behave-tools: linters
	scripts/install-behave-tools.sh

# Opt-in behavioural harness: runs real engines on the Kiri container runtime. It is not part of `make check`.
# Narrow a run with BEHAVE_ONLY=<source>/<fixture> (regexp) and BEHAVE_TARGETS=<provider>,<provider>.
behave: behave-tools
	go test -tags behave -count=1 -timeout 3h -v -run TestBehave ./internal/behave
