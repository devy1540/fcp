.PHONY: build compatibility coverage test vet verify run

build:
	go build -trimpath -o bin/fcp ./cmd/fcp

compatibility:
	go run ./cmd/compatibility-doc

coverage:
	@coverage_dir="$$(mktemp -d "$${TMPDIR:-/tmp}/fcp-coverage.XXXXXX")"; \
	trap 'find "$$coverage_dir" -type f -delete; rmdir "$$coverage_dir"' EXIT; \
	profile="$$coverage_dir/core.out"; \
	cli_profile="$$coverage_dir/cli.out"; \
	server_profile="$$coverage_dir/server.out"; \
	state_profile="$$coverage_dir/state.out"; \
	go test -count=1 -covermode=atomic -coverpkg=./... -coverprofile="$$profile" ./...; \
	go test -count=1 -covermode=atomic -coverprofile="$$cli_profile" ./internal/cli; \
	go test -count=1 -covermode=atomic -coverprofile="$$server_profile" ./internal/server; \
	go test -count=1 -covermode=atomic -coverprofile="$$state_profile" ./internal/state; \
	./scripts/check-coverage.sh "$$profile" 80.0 "cross-package Go coverage"; \
	./scripts/check-coverage.sh "$$cli_profile" 80.0 "CLI package coverage"; \
	./scripts/check-coverage.sh "$$server_profile" 80.0 "server package coverage"; \
	./scripts/check-coverage.sh "$$state_profile" 85.0 "state package coverage"

test:
	go test ./...

vet:
	go vet ./...

verify:
	$(MAKE) coverage
	go test -count=1 -race ./...
	go vet ./...
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
	node --check internal/server/dashboard/app.js
	node --test scripts/verify-pnpm-audit.test.mjs
	docker compose config -q

run:
	go run ./cmd/fcp
