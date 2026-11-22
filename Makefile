.PHONY: test chains lint lean vectors check linters

test:
	go test ./...

# The full chain matrix; `make check` runs a deterministic sample of it.
chains:
	go test ./internal/roundtrip -run TestChainMatrix -count=1

lint:
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
