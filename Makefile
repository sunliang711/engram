.PHONY: build test clean run

BINARY_DIR := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	@mkdir -p $(BINARY_DIR)
	go build -trimpath -ldflags="-X engram/internal/version.Version=$(VERSION)" -o $(BINARY_DIR)/engram-server ./cmd/engram-server

test:
	go test -v -race ./...

run: build
	./$(BINARY_DIR)/engram-server --config config.example.toml

clean:
	rm -rf $(BINARY_DIR) export backups
