.PHONY: build run test clean install type-check

build:
	go build -o bin/az ./cmd/az

run:
	go run ./cmd/az

test:
	go test -v ./...

test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

type-check:
	go build ./...

clean:
	rm -rf bin/ coverage.out coverage.html

install:
	go install ./cmd/az

lint:
	golangci-lint run ./...
