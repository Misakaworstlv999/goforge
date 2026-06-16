# GoForge — common developer tasks.
# Run `make help` for a summary.

BINARY := goforge
PKG    := ./cmd/goforge
# Extra args passed to `make run`, e.g. make run ARGS="-mode chat"
ARGS   ?= -mode agent

.DEFAULT_GOAL := help

.PHONY: help build run test race fmt vet verify clean

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## Build the single binary into ./$(BINARY)
	go build -o $(BINARY) $(PKG)

run: ## Run the CLI (override mode with ARGS, e.g. ARGS="-mode chat")
	go run $(PKG) $(ARGS)

test: ## Run all tests
	go test ./... -count=1

race: ## Run all tests with the race detector
	go test ./... -race -count=1

fmt: ## Format all Go sources
	gofmt -s -w .

vet: ## Run go vet
	go vet ./...

verify: ## Full baseline check (desensitization + build + fmt + vet + test)
	./init.sh

clean: ## Remove the built binary
	rm -f $(BINARY)
