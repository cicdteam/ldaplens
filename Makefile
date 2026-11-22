MODULE := $(shell go list -m)
GO_SRC := cmd internal web/embed.go deploy/demo-ldif/photos

# Minimum tool versions. Local installs must meet or exceed these; older or
# missing tools fall back to Docker at exactly these versions.
GOFUMPT_MIN := 0.7.0
GOIMPORTS_VERSION := 0.52.0
GOLANGCI_LINT_MIN := 2.15.0
GOVULNCHECK_MIN := 1.9.0
HELM_MIN := 4.3.2
KUBECONFORM_MIN := 0.8.0
# Dependabot does not see GO_IMAGE: bump it with the Dockerfile's golang line.
GO_IMAGE := golang:1.27.4@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf
IMAGE ?= ldaplens:dev
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# VERSION is exported so recipes quote it as "$$VERSION": the shell never parses a tag name.
export GOFUMPT_MIN GOIMPORTS_VERSION GOLANGCI_LINT_MIN GOVULNCHECK_MIN HELM_MIN KUBECONFORM_MIN GO_IMAGE VERSION

.PHONY: help fmt vet lint vuln test test-integration build dist image image-multiarch check web web-check chart-check chart-kind e2e demo-start demo-stop

.DEFAULT_GOAL := help

DEMO_PORT ?= 8080
DEMO_COMPOSE := DEMO_PORT=$(DEMO_PORT) docker compose -f deploy/docker-compose.yml
# 127.0.0.1, not localhost: the port is published on IPv4 only, and localhost
# may resolve to ::1 first, where another program can hold the same port.
DEMO_URL := http://127.0.0.1:$(DEMO_PORT)/?token=demo-token-not-secret

help: ## Show the targets and what they do
	@awk 'BEGIN {FS = ":.*## "} /^[a-z0-9-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Go code (gofumpt, goimports)
	scripts/tool.sh gofumpt -l -w $(GO_SRC)
	scripts/tool.sh goimports -local $(MODULE) -w $(GO_SRC)

vet: ## Run go vet, with and without the integration tag
	go vet ./...
	go vet -tags integration ./...

lint: ## Run golangci-lint
	scripts/tool.sh golangci-lint run --timeout 5m

vuln: ## Check Go dependencies with govulncheck
	scripts/tool.sh govulncheck ./...

test: ## Run Go unit tests with the race detector
	go test -race ./...

test-integration: ## Run Go integration tests against OpenLDAP and Samba AD in Docker
	go test -race -tags integration -count=1 ./...

web: ## Build the web UI
	cd web && npm ci --ignore-scripts && npm run build

web-check: ## Type-check, lint, format-check and unit-test the web UI; npm audit of production deps
	cd web && npm ci --ignore-scripts && npm run check && npm audit --omit=dev --audit-level=high

chart-check: ## Lint the Helm chart, validate its renders with kubeconform, check its refusals
	scripts/chart-check.sh

chart-kind: ## Install the Helm chart in a kind cluster and sign in through it (needs Docker, kind, kubectl)
	scripts/chart-kind.sh

e2e: ## Run the Playwright e2e tests against OpenLDAP and Samba AD in Docker
	cd web && npm ci --ignore-scripts && npm run e2e

build: web ## Build bin/ldaplens with the embedded UI
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$$VERSION" -o bin/ldaplens ./cmd/ldaplens

# Release archives named as uname -s and uname -m print, so the quick start
# needs no mapping, and without the version, so
# releases/latest/download/<name> stays one URL. GOOS/GOARCH:uname.
DIST_PLATFORMS := linux/amd64:Linux_x86_64 linux/arm64:Linux_aarch64 darwin/arm64:Darwin_arm64 darwin/amd64:Darwin_x86_64
dist: web ## Build the release archives and checksums into dist/
	rm -rf dist && mkdir -p dist
	set -e; for p in $(DIST_PLATFORMS); do \
	  t=$${p%:*}; n=ldaplens_$${p#*:}; mkdir -p dist/$$n; \
	  CGO_ENABLED=0 GOOS=$${t%/*} GOARCH=$${t#*/} go build -trimpath -ldflags="-s -w -X main.version=$$VERSION" -o dist/$$n/ldaplens ./cmd/ldaplens; \
	  cp LICENSE README.md dist/$$n/; \
	  tar -czf dist/$$n.tar.gz -C dist/$$n ldaplens LICENSE README.md; \
	  rm -r dist/$$n; \
	done
	cd dist && shasum -a 256 *.tar.gz >ldaplens_checksums.txt

image: ## Build the Docker image (IMAGE=ldaplens:dev by default)
	docker build -f deploy/Dockerfile --build-arg VERSION="$$VERSION" -t $(IMAGE) .

PLATFORMS ?= linux/amd64 linux/arm64

image-multiarch: ## Build and check the image for amd64 and arm64 (IMAGE-amd64, IMAGE-arm64)
	@set -e; for p in $(PLATFORMS); do \
	  a=$${p#linux/}; \
	  docker buildx build --platform $$p --load --build-arg VERSION="$$VERSION" \
	    -f deploy/Dockerfile -t "$(IMAGE)-$$a" . ; \
	  deploy/image-check.sh "$(IMAGE)-$$a" $$p "$$VERSION"; \
	done

docs-check: ## Check that every relative link in the Markdown files names a file and heading that exist
	node scripts/md-links.mjs

check: fmt vet lint vuln test web-check chart-check docs-check ## Run every check: fmt vet lint vuln test web-check chart-check docs-check

demo-start: ## Start the demo: OpenLDAP and Samba AD with sample data, and ldaplens (DEMO_PORT=8080)
	$(DEMO_COMPOSE) up -d --build --wait
	@echo
	@echo "ldaplens demo: $(DEMO_URL)"
	@echo "Profiles: demo (editable), demo-readonly (read-only), demo-ad (Samba AD, editable). Stop with: make demo-stop"

demo-stop: ## Stop the demo (the directories reset on the next start)
	$(DEMO_COMPOSE) rm -fsv
	$(DEMO_COMPOSE) down
