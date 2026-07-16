.PHONY: build test fmt vet lint run-api docker-build

build:
	go build ./...

test:
	go test -count=1 ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet:
	go vet ./...

lint:
	golangci-lint run

run-api:
	go run ./cmd/marketplace-api

docker-build:
	docker build -f Dockerfile.api -t octo-marketplace-api:local .
