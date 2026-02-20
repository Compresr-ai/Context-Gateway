.PHONY: build run test clean docker dev dev-debug embed-prep

# Build variables
BINARY_NAME=context-gateway
BUILD_DIR=bin
MAIN_PATH=./cmd
DEFAULT_CONFIG=configs/preemptive_summarization.yaml

# Go variables
GOCMD=go
GOBUILD=$(GOCMD) build
GORUN=$(GOCMD) run
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

# Build the binary
build: embed-prep
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Run the application
run:
	$(GORUN) $(MAIN_PATH)

# Run with config (usage: make run-config CONFIG=configs/tool_output_passthrough.yaml)
CONFIG ?= configs/tool_output_passthrough.yaml
run-config:
	$(GORUN) $(MAIN_PATH) serve --config configs/config.yaml

# Run tests
test:
	$(GOTEST) -v ./...

# Run tests with coverage (HTML report)
coverage:
	@echo "Running tests with coverage..."
	$(GOTEST) ./... -coverprofile=tests/coverage.out -coverpkg=./internal/...
	$(GOCMD) tool cover -html=tests/coverage.out -o tests/coverage.html
	$(GOCMD) tool cover -func=tests/coverage.out | tail -1
	@echo "\n✅ Coverage report: tests/coverage.html"
	@open tests/coverage.html

# Run tests with coverage (legacy - root folder)
test-coverage:
	$(GOTEST) -v -cover -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out coverage.html
	@rm -f tests/coverage.out tests/coverage.html
	@echo "Clean complete"

# Download dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy

# Build Docker image
docker:
	docker build -t context-gateway:latest .

# Run with Docker Compose
docker-up:
	docker-compose up -d

# Stop Docker Compose
docker-down:
	docker-compose down

# Prepare embedded files (required for go:embed)
embed-prep:
	@mkdir -p cmd/agents cmd/configs
	@cp -f agents/*.yaml cmd/agents/ 2>/dev/null || true
	@cp -f configs/*.yaml cmd/configs/ 2>/dev/null || true

# Format code
fmt:
	$(GOCMD) fmt ./...

# Lint code
lint:
	golangci-lint run

# Security scan (same as CI)
security:
	@echo "Running security scan..."
	gosec -exclude-dir=tests -exclude=G104,G304 ./...
	govulncheck ./...

# Dev: build and run in foreground with default config
dev: build
	@echo "Starting gateway (foreground)..."
	./$(BUILD_DIR)/$(BINARY_NAME) serve --config $(DEFAULT_CONFIG)

# Dev with debug logging
dev-debug: build
	@echo "Starting gateway (foreground, debug)..."
	./$(BUILD_DIR)/$(BINARY_NAME) serve --config $(DEFAULT_CONFIG) --debug

# Build for multiple platforms
build-all:
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	GOOS=darwin GOARCH=arm64 $(GOBUILD) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)
	GOOS=windows GOARCH=amd64 $(GOBUILD) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PATH)

# Help
help:
	@echo "Available targets:"
	@echo "  dev          - Build and run in foreground (default config)"
	@echo "  dev-debug    - Build and run in foreground with debug logging"
	@echo "  build        - Build the binary"
	@echo "  run          - Run the application"
	@echo "  run-config   - Run with config file"
	@echo "  test         - Run tests"
	@echo "  test-coverage- Run tests with coverage"
	@echo "  clean        - Clean build artifacts"
	@echo "  deps         - Download dependencies"
	@echo "  docker       - Build Docker image"
	@echo "  docker-up    - Start with Docker Compose"
	@echo "  docker-down  - Stop Docker Compose"
	@echo "  fmt          - Format code"
	@echo "  lint         - Lint code"
	@echo "  build-all    - Build for all platforms"
