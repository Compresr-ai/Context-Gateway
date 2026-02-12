# Contributing to Context Gateway

Thank you for your interest in contributing! This document provides guidelines for contributing.

## Development Setup

### Prerequisites
- Go 1.23+
- golangci-lint (for linting)
- Docker (optional, for container builds)

### Quick Start

```bash
# Clone the repository
git clone https://github.com/compresr/context-gateway.git
cd context-gateway

# Install dependencies
go mod download

# Run tests
make test

# Run linter
make lint

# Build
make build
```

## Testing

### Unit Tests (No API Keys Required)
```bash
# Run all unit tests
go test ./tests/anthropic/unit/... ./tests/openai/unit/... ./tests/common/...

# Run with coverage
make coverage
```

### Integration Tests (API Keys Required)
```bash
# Copy and configure environment
cp .env.example .env
# Edit .env with your API keys

# Run integration tests
go test ./tests/anthropic/integration/...
```

## Code Style

- Run `make fmt` before committing
- Run `make lint` to check for issues
- Follow existing patterns in the codebase

## Pull Request Process

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Make your changes
4. Run tests (`make test`)
5. Run linter (`make lint`)
6. Commit your changes (`git commit -m 'Add amazing feature'`)
7. Push to the branch (`git push origin feature/amazing-feature`)
8. Open a Pull Request

## Security

- Never commit API keys or secrets
- Use `.env` for local development (it's in `.gitignore`)
- Report security issues privately to the maintainers

## Questions?

Open an issue for questions or discussions.
