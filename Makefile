.PHONY: test lint lean vectors check linters

test:
	go test ./...

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
check: test lint
	scripts/check-vectors.sh
	scripts/lean-axioms.sh
	scripts/lean-currency.sh
