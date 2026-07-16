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

OPENAPI_MAIN := cmd/marketplace-api/main.go
OPENAPI_SCAN_DIRS := internal/api/handler/mcp.go internal/api/handler/mcp_icon.go internal/api/handler/admin_mcp.go internal/api/handler/skill/handler.go internal/api/handler/upload/handler.go internal/api/handler/category/handler.go internal/api/handler/category/admin.go

# OpenAPI toolchain (installed by octo-openapi-dev-skill main)
include tools/octo-api/assets/openapi.mk
