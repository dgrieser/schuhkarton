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

GORELEASER_VERSION ?= v2.13.3
GORELEASER := $(shell command -v goreleaser 2>/dev/null || echo "$(GO) run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)")
TARGETS := linux/amd64 linux/arm64 linux/arm/7 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build
build: ## Build ./schuhkarton (UI embedded)
	$(GO) build -trimpath -ldflags "-s -w" -o $(BINARY) $(MAIN)

.PHONY: build-all
build-all: ## Cross-compile every release target (CGO off) into dist/
	@for t in $(TARGETS); do \
		os=$${t%%/*}; rest=$${t#*/}; arch=$${rest%%/*}; arm=$$( [ "$$rest" != "$$arch" ] && echo $${rest#*/} ); \
		ext=$$( [ $$os = windows ] && echo .exe ); \
		echo "  $$t"; \
		GOOS=$$os GOARCH=$$arch GOARM=$$arm CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" \
			-o dist/$(BINARY)_$${os}_$${arch}$${arm:+v$$arm}$$ext $(MAIN) || exit 1; \
	done

.PHONY: snapshot
snapshot: ## GoReleaser snapshot build (no publishing)
	$(GORELEASER) release --snapshot --clean

.PHONY: release
release: ## Publish the release for the current tag (needs GITHUB_TOKEN)
	$(GORELEASER) release --clean

.PHONY: faces-image
faces-image: ## Build the face detection container (schuhkarton-faces)
	docker build -f Dockerfile.faces -t schuhkarton-faces .

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

.PHONY: vuln
vuln: ## Known vulnerabilities the code is affected by (govulncheck)
	@# built with the Go version of go.mod: a govulncheck built with an older one cannot read the code
	GOTOOLCHAIN=$$($(GO) env GOVERSION) $(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BINARY) dist
