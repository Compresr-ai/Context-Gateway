.PHONY: build run test test-unit test-race test-perf test-perf-quick test-bench test-mem test-stress clean docker dev dev-debug embed-prep build-dashboard docker-test-build docker-test-up docker-test-down docker-test-go docker-test-agents docker-test-e2e

# Build variables
BINARY_NAME=context-gateway
BUILD_DIR=bin
MAIN_PATH=./cmd
DEFAULT_CONFIG=configs/preemptive_summarization.yaml
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-s -w -X main.Version=$(VERSION)

# Go variables
GOCMD=go
GOBUILD=$(GOCMD) build
GORUN=$(GOCMD) run
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

# Build the React dashboard into cmd/dashboard_dist/ (required before go build)
build-dashboard:
	@echo "Building dashboard assets..."
	@cd web/dashboard && npm run build
	@echo "Dashboard build complete: cmd/dashboard_dist/"

# Build the binary (includes dashboard assets via go:embed)
build: embed-prep build-dashboard
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Run the application
run:
	$(GORUN) $(MAIN_PATH)

# Run with config (usage: make run-config CONFIG=configs/tool_output_passthrough.yaml)
CONFIG ?= configs/tool_output_passthrough.yaml
run-config:
	$(GORUN) $(MAIN_PATH) serve --config configs/config.yaml

# Run tests (unit + short integration; skips slow real-API E2E tests)
test:
	$(GOTEST) -short -timeout 120s ./...

# Run unit tests only (all packages, -short flag skips slow real-API tests)
test-unit:
	$(GOTEST) -short -timeout 120s -count=1 ./...

# Run tests with race detector (unit mode)
test-race:
	$(GOTEST) -short -race -timeout 180s ./...

# Run performance tests (memory, CPU, stress)
test-perf:
	@echo "Running performance tests..."
	$(GOTEST) -v -timeout 300s ./tests/performance/
	@echo "✅ Performance tests complete"

# Run quick performance tests (skip stress/leak tests)
test-perf-quick:
	@echo "Running quick performance tests..."
	$(GOTEST) -v -short -timeout 60s ./tests/performance/
	@echo "✅ Quick performance tests complete"

# Run performance benchmarks
test-bench:
	@echo "Running performance benchmarks..."
	$(GOTEST) -v -bench=. -benchmem -benchtime=5s ./tests/performance/ | tee benchmark-results.txt
	@echo "✅ Benchmark results saved to benchmark-results.txt"

# Test memory footprint only
test-mem:
	@echo "Testing memory footprint..."
	$(GOTEST) -v -timeout 60s -run TestMemoryFootprint ./tests/performance/

# Run stress test only
test-stress:
	@echo "Running stress test..."
	$(GOTEST) -v -timeout 300s -run TestStressLoad ./tests/performance/

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
	gosec -exclude-dir=tests ./...
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
	GOOS=linux GOARCH=amd64 $(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	GOOS=darwin GOARCH=arm64 $(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)
	GOOS=windows GOARCH=amd64 $(GOBUILD) -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PATH)

# =============================================================================
# Docker E2E Test Infrastructure
# =============================================================================

DOCKER_COMPOSE_TEST=docker compose -f docker/docker-compose.test.yaml

# Build all Docker test images
docker-test-build:
	@echo "Building Docker test images..."
	$(DOCKER_COMPOSE_TEST) build

# Start provider + gateway containers (no agents)
docker-test-up:
	@echo "Starting test infrastructure..."
	$(DOCKER_COMPOSE_TEST) up -d ollama litellm gateway
	@echo "Waiting for services to be healthy..."
	@sleep 10
	@echo "Services started. Run 'make docker-test-go' or 'make docker-test-agents'"

# Stop and remove all test containers
docker-test-down:
	@echo "Stopping test infrastructure..."
	$(DOCKER_COMPOSE_TEST) down -v --remove-orphans

# Run ALL Go integration tests against Docker services
# Cloud tests (Anthropic, OpenAI, Gemini) auto-skip without API keys
docker-test-go:
	@echo "Running ALL Go integration tests..."
	OLLAMA_URL=http://localhost:11434 \
	LITELLM_URL=http://localhost:4000 \
	LITELLM_API_KEY=sk-test-litellm-key \
	$(GOTEST) -v \
		./tests/ollama/integration/... \
		./tests/litellm/integration/... \
		./tests/bedrock/integration/... \
		./tests/agents/integration/... \
		./tests/anthropic/integration/... \
		./tests/openai/integration/... \
		./tests/gemini/integration/...

# Run agent simulator containers
docker-test-agents:
	@echo "Running agent simulators..."
	$(DOCKER_COMPOSE_TEST) up --exit-code-from agent-ollama-direct agent-ollama-direct
	$(DOCKER_COMPOSE_TEST) up --exit-code-from agent-litellm-proxy agent-litellm-proxy
	$(DOCKER_COMPOSE_TEST) up --exit-code-from agent-bedrock agent-bedrock
	$(DOCKER_COMPOSE_TEST) up --exit-code-from agent-cursor agent-cursor
	$(DOCKER_COMPOSE_TEST) up --exit-code-from agent-opencode agent-opencode
	@echo "All agent tests passed"

# Full E2E pipeline: build -> up -> test -> agents -> down
docker-test-e2e: docker-test-build docker-test-up
	@echo "=== Running full E2E test pipeline ==="
	@sleep 5
	$(MAKE) docker-test-go || ($(MAKE) docker-test-down && exit 1)
	$(MAKE) docker-test-agents || ($(MAKE) docker-test-down && exit 1)
	$(MAKE) docker-test-down
	@echo "=== E2E test pipeline complete ==="

# Help
help:
	@echo "Available targets:"
	@echo "  dev              - Build and run in foreground (default config)"
	@echo "  dev-debug        - Build and run in foreground with debug logging"
	@echo "  build            - Build the binary"
	@echo "  run              - Run the application"
	@echo "  run-config       - Run with config file"
	@echo "  test             - Run tests (short mode, skips slow E2E)"
	@echo "  test-unit        - Run unit tests only (alias for test)"
	@echo "  test-race        - Run tests with race detector"
	@echo "  test-coverage    - Run tests with coverage"
	@echo "  clean            - Clean build artifacts"
	@echo "  deps             - Download dependencies"
	@echo "  docker           - Build Docker image"
	@echo "  docker-up        - Start with Docker Compose"
	@echo "  docker-down      - Stop Docker Compose"
	@echo "  fmt              - Format code"
	@echo "  lint             - Lint code"
	@echo "  build-all        - Build for all platforms"
	@echo ""
	@echo "Docker E2E Testing:"
	@echo "  docker-test-e2e    - Full E2E pipeline (build, up, test, down)"
	@echo "  docker-test-build  - Build Docker test images"
	@echo "  docker-test-up     - Start provider + gateway containers"
	@echo "  docker-test-down   - Stop all test containers"
	@echo "  docker-test-go     - Run Go integration tests"
	@echo "  docker-test-agents - Run agent simulator containers"
