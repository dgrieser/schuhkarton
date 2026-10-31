# Development tasks for schuhkarton. Bare `make` builds; `make help` lists targets.

BINARY  := schuhkarton
MAIN    := ./cmd/schuhkarton
GO      ?= go
PREFIX  ?= $(HOME)/bin
# Folders for `make run` / `make dev`
PHOTOS  ?= $(HOME)/Downloads/Bilder
LISTEN  ?= 127.0.0.1:8088

.DEFAULT_GOAL := build

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build ./schuhkarton (UI embedded)
	$(GO) build -trimpath -ldflags "-s -w" -o $(BINARY) $(MAIN)

.PHONY: install
install: build ## Install the binary into $(PREFIX)
	install -Dm755 $(BINARY) $(PREFIX)/$(BINARY)

.PHONY: run
run: build ## Serve $(PHOTOS) on $(LISTEN)
	./$(BINARY) -listen $(LISTEN) $(PHOTOS)

.PHONY: dev
dev: build ## Like run, but the UI is read from ./web on every request
	./$(BINARY) -listen $(LISTEN) -web web $(PHOTOS)

.PHONY: test
test: ## Run the tests
	$(GO) test ./...

.PHONY: check
check: ## gofmt, vet, node syntax check, tests
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	$(GO) vet ./...
	@command -v node >/dev/null && node --check web/app.js || true
	$(GO) test ./...

.PHONY: clean
clean: ## Remove build output
	rm -f $(BINARY)
