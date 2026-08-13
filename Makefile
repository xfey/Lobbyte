SHELL := /bin/sh

.PHONY: build check-build check-npm check-npm-template check-release-boundary fmt fuzz npm-candidate run test vuln

build:
	@mkdir -p .tmp/bin
	@go build -trimpath -o .tmp/bin/lobbyte ./cmd/lobbyte

check-build:
	@./scripts/check-build.sh

check-npm-template:
	@node --test packaging/npm/launcher.test.js

check-release-boundary:
	@./scripts/check-release-boundary.sh

npm-candidate:
	@./scripts/build-npm-candidate.sh

check-npm:
	@candidate="$$(./scripts/build-npm-candidate.sh)"; \
		first_checksum="$$(shasum -a 256 "$$candidate" | cut -d ' ' -f 1)"; \
		candidate="$$(./scripts/build-npm-candidate.sh)"; \
		second_checksum="$$(shasum -a 256 "$$candidate" | cut -d ' ' -f 1)"; \
		test "$$first_checksum" = "$$second_checksum"; \
		./scripts/test-npm-candidate.sh "$$candidate"

fmt:
	@gofmt -w $$(find cmd internal -type f -name '*.go' | sort)

fuzz:
	@./scripts/fuzz.sh

vuln:
	@go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

test: check-release-boundary check-build check-npm-template
	@unformatted="$$(gofmt -l $$(find cmd internal -type f -name '*.go' | sort))"; \
	if [ -n "$$unformatted" ]; then \
		echo "Go files need formatting:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@go vet ./...
	@go test ./...
	@go test -race ./...

run:
	@go run ./cmd/lobbyte
