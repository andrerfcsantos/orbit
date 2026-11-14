# Make targets for building, running, checking and releasing orbit.
#
# scripts/build.sh remains the source of truth for a build: targets here call it
# rather than restating what it does, so `make build` and `scripts/build.sh`
# cannot drift apart. Run `make` or `make help` to list the targets.
#
# Variables can be overridden on the command line, e.g.
#   make run ARGS="git list ~/code"
#   make release-tag BUMP=patch

SHELL := bash
.DEFAULT_GOAL := help

BIN        ?= $(or $(OUTPUT),bin/orb$(shell go env GOEXE))
# Arguments for the CLI (run).
ARGS       ?=

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE       ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
# Read by recipes from the environment rather than expanded into them, so a tag
# name holding shell metacharacters stays data. scripts/build.sh reads them too.
export VERSION COMMIT DATE

# Release bump for release-tag: patch, minor or major.
BUMP       ?= minor
# orbit's own release, whatever ORBIT_GIT_RELEASE_TAG_* or a config file say.
RELEASE_TAG_FLAGS = --branch main --remote origin --prefix v --bump $(BUMP) --pre "" --push=true --message ""

##@ General

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN { FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m [VAR=value ...]\n" } \
		/^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Build

.PHONY: build
build: ## Build bin/orb, with the version, commit and date stamped in.
	scripts/build.sh -o "$(BIN)"

.PHONY: install
install: ## Build and go install orb into $GOBIN (or $GOPATH/bin), version stamped in.
	scripts/build.sh --install -o "$(BIN)"

.PHONY: clean
clean: ## Remove bin/ and dist/.
	rm -rf bin dist

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum.
	go mod tidy

.PHONY: deps-upgrade
deps-upgrade: ## Upgrade every Go module to its latest version, then tidy.
	go get -u -t ./...
	go mod tidy

##@ Run

.PHONY: run
run: ## Run the CLI from source: make run ARGS="git list ~/code".
	go run ./cmd/orb $(ARGS)

.PHONY: version
version: build ## Build, then print the version stamped in.
	"$(if $(findstring /,$(BIN)),$(BIN),./$(BIN))" version

##@ Test and check

.PHONY: check
check: lint test test-race ## Run what CI runs: gofmt, go vet, the tests and the race tests.

.PHONY: test
test: ## Run the Go tests, including the architecture rules.
	go test ./...

.PHONY: test-race
test-race: ## Run the Go tests with the race detector.
	go test -race ./...

.PHONY: lint
lint: vet ## Check gofmt and run go vet (what CI's lint step runs).
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: fmt
fmt: ## Format the Go sources.
	gofmt -w .

##@ Release

.PHONY: release-tag-dry-run
release-tag-dry-run: ## Show the next release tag: make release-tag-dry-run BUMP=patch.
	go run ./cmd/orb git release-tag $(RELEASE_TAG_FLAGS) --dry-run

.PHONY: release-tag
release-tag: ## Create and push the next release tag from origin/main (starts a release).
	go run ./cmd/orb git release-tag $(RELEASE_TAG_FLAGS) --yes

.PHONY: release-check
release-check: ## Validate .goreleaser.yaml.
	goreleaser check

.PHONY: release-snapshot
release-snapshot: ## Build every release archive locally into dist/, publishing nothing.
	goreleaser release --snapshot --clean
