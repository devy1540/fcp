.PHONY: build test vet run

build:
	go build -trimpath -o bin/fcp ./cmd/fcp

test:
	go test ./...

vet:
	go vet ./...

run:
	go run ./cmd/fcp
