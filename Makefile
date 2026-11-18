.PHONY: build test clean run

BINARY_DIR := bin

build:
	@mkdir -p $(BINARY_DIR)
	go build -trimpath -o $(BINARY_DIR)/memory-server ./cmd/memory-server

test:
	go test -v -race ./...

run: build
	./$(BINARY_DIR)/memory-server --config config.example.toml

clean:
	rm -rf $(BINARY_DIR) export backups
