.PHONY: lint lint-fix fmt test build run vet tidy

# Run all linters defined in .golangci.yml.
lint:
	golangci-lint run --timeout 3m

# Auto-fix what the linters can fix (gofmt, goimports, gci, some staticcheck QF rules).
lint-fix:
	golangci-lint fmt
	golangci-lint run --fix --timeout 3m

# Format-only — runs the gofmt / goimports / gci formatters configured in
# .golangci.yml.
fmt:
	golangci-lint fmt

# go vet without the rest of the linter pipeline.
vet:
	go vet ./...

# Run all unit + integration tests.
test:
	go test ./...

# Run tests with race detector.
test-race:
	go test -race ./...

# Tidy + verify go.mod / go.sum.
tidy:
	go mod tidy
	go mod verify

# Compile the binary.
build:
	go build -o bin/financentury .

# Run with the .env file loaded (godotenv handles missing files).
run:
	go run .
