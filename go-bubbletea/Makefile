.PHONY: build run test test-coverage type-check clean install lint g5-validate g5-checklist test-scripts

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

g5-validate:
	./scripts/g5-validation.sh

g5-checklist:
	./scripts/g5-complete.sh

test-scripts:
	./scripts/g5-validation_test.sh
