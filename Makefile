.DEFAULT_GOAL := help

GO              ?= go
BIN             := bin/pgrelay
PKG             := ./cmd/pgrelay
GOVULNCHECK_VER ?= v1.1.4
MIGRATE_VER     ?= v4.19.1
DATABASE_URL    ?= postgres://pgrelay:pgrelay@localhost:5432/pgrelay?sslmode=disable
VERSION         ?= dev
# Recursive (=) so the git invocation only fires when COMMIT is actually expanded.
COMMIT           = $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS          = -X main.Version=$(VERSION) -X main.Commit=$(COMMIT)

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the pgrelay binary into ./bin
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

.PHONY: test
test: ## Run unit tests with race detector.
	$(GO) test -race -count=1 ./...

.PHONY: test-integration
test-integration: ## Run unit + integration tests (requires Docker).
	$(GO) test -race -tags=integration -count=1 ./...

.PHONY: coverage
coverage: ## Run unit tests and write coverage profile to coverage.out.
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: check
check: fmt lint vuln test ## Run fmt + lint + vuln + test in sequence.

.PHONY: lint
lint: ## Run golangci-lint.
	golangci-lint run ./...

.PHONY: fmt
fmt: ## Format Go source files.
	$(GO) fmt ./...

.PHONY: vuln
vuln: ## Run govulncheck.
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VER) ./...

.PHONY: docker
docker: ## Build the Docker image.
	docker build -t pgrelay:dev \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		.

export DATABASE_URL

.PHONY: migrate-up
migrate-up: ## Apply all pending migrations against $DATABASE_URL.
	@$(GO) run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VER) \
		-path ./migrations -database "$$DATABASE_URL" up

.PHONY: migrate-down
migrate-down: ## Roll back the most recent migration against $DATABASE_URL.
	@$(GO) run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VER) \
		-path ./migrations -database "$$DATABASE_URL" down 1

.PHONY: tidy
tidy: ## Run go mod tidy.
	$(GO) mod tidy

.PHONY: clean
clean: ## Remove build artifacts.
	rm -rf bin/
