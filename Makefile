.PHONY: test lint lean vectors check

test:
	go test ./...

lint:
	golangci-lint run

lean:
	cd lean4 && lake build

vectors:
	cd lean4 && lake exe gen-vectors ../testdata/lean-vectors

check: test lint
	scripts/check-vectors.sh
	scripts/lean-axioms.sh
