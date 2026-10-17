BINARY  := brother-scan-server
PKG     := ./cmd/brother-scan-server
IMAGE   ?= ghcr.io/aurimasniekis/go-brother-scan-to-network-server
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
CONFIG  ?= config.yaml

.PHONY: all build run test vet fmt lint tidy clean docker docker-run help integration-servers integration-test integration-stop

all: fmt vet test build ## Format, vet, test and build

build: ## Build the binary into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) $(PKG)

run: build ## Build and run with $(CONFIG)
	./bin/$(BINARY) -config $(CONFIG)

test: ## Run unit tests
	go test -race ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format the code
	gofmt -w .

lint: ## Check formatting and vet (CI)
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...

tidy: ## Tidy go.mod
	go mod tidy

clean: ## Remove build output
	rm -rf bin

docker: ## Build the Docker image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

docker-run: ## Run the Docker image with host networking and ./data
	docker run --rm -it --network host -v $(CURDIR)/data:/data -v $(CURDIR)/scans:/scans $(IMAGE):latest

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

integration-servers: ## Start Samba, FTP and S3 (s3mock) containers for integration tests
	docker run -d --rm --name bstn-it-smb -p 1445:445 dperson/samba -p -u "scan;secret" -s "scans;/share;yes;no;no;scan"
	docker run -d --rm --name bstn-it-ftp -p 2121:21 -p 21000-21010:21000-21010 -e USERS="scan|secret" -e ADDRESS=127.0.0.1 delfer/alpine-ftp-server
	docker run -d --rm --name bstn-it-s3 -p 9000:9090 -e initialBuckets=scans adobe/s3mock

integration-test: ## Run destination integration tests (needs integration-servers)
	DEST_IT=1 go test -count=1 -run Integration -v ./internal/destination/

integration-stop: ## Stop the integration test containers
	-docker rm -f bstn-it-smb bstn-it-ftp bstn-it-s3
