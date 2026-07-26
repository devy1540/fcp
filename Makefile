.PHONY: build compatibility coverage test vet verify run

build:
	go build -trimpath -o bin/fcp ./cmd/fcp

compatibility:
	go run ./cmd/compatibility-doc

coverage:
	@profile="$$(mktemp "$${TMPDIR:-/tmp}/fcp-coverage.XXXXXX")"; \
	trap 'find "$$profile" -delete' EXIT; \
	go test -count=1 -covermode=atomic -coverpkg=./... -coverprofile="$$profile" ./...; \
	./scripts/check-coverage.sh "$$profile" 72.0 "cross-package Go coverage"

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
