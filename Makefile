.PHONY: build run test lint clean install dev

# Build the binary
build:
	go build -o bin/symb ./cmd/symb

# Run the application
run:
	go run ./cmd/symb

# Run tests
test:
	go test -v -race -coverprofile=coverage.out ./...

# Run linter
lint:
	$$(go env GOPATH)/bin/golangci-lint run ./cmd/... ./internal/...

# Clean build artifacts
clean:
	rm -rf bin/
	rm -f coverage.out

# Install dependencies
install:
	go mod download
	go mod tidy

# Development mode (with hot reload if you add air later)
dev: install
	go run ./cmd/symb

# Show coverage
coverage: test
	go tool cover -html=coverage.out
