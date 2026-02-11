.PHONY: build run test lint clean install dev

# Build the binary
build:
	go build -o bin/smith ./cmd/smith

# Run the application
run:
	go run ./cmd/smith

# Run tests
test:
	go test -v -race -coverprofile=coverage.out ./...

# Run linter
lint:
	golangci-lint run

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
	go run ./cmd/smith

# Show coverage
coverage: test
	go tool cover -html=coverage.out
