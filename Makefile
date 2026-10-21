BINARY  := mig-scaler
IMAGE   ?= mig-scaler
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOOS    := $(or $(GOOS),$(shell go env GOOS))
GOARCH  := $(or $(GOARCH),$(shell go env GOARCH))
DIST    := dist/$(BINARY)-$(VERSION)-$(GOOS)-$(GOARCH)

.PHONY: help tidy fmt vet lint test build tools run dist docker-build docker-push clean

help: ## Show targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

tidy: ## Update go.mod and go.sum
	go mod tidy

fmt: ## Format the code
	gofmt -w .

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

test: ## Run tests
	go test -race ./...

build: ## Build the binary in bin/ (GOOS and GOARCH can be set)
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

tools: ## Build the standalone tools in bin/ (scaleset-delete)
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./tools/...

run: build ## Build and run, pass flags with ARGS="..."
	./bin/$(BINARY) $(ARGS)

dist: build tools ## Package the binary, README and startup.sh in dist/*.tar.gz
	rm -rf $(DIST) && mkdir -p $(DIST)
	cp bin/$(BINARY) bin/scaleset-delete README.md startup.sh $(DIST)/
	tar -czf $(DIST).tar.gz -C dist $(notdir $(DIST))
	rm -rf $(DIST)
	@echo $(DIST).tar.gz

docker-build: ## Build the Docker image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

docker-push: docker-build ## Push the Docker image
	docker push $(IMAGE):$(VERSION)

clean: ## Remove build output
	rm -rf bin/ dist/
