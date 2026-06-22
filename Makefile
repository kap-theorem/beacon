BINARY_DIR := bin
EMAIL_WORKER_BIN := $(BINARY_DIR)/email_worker
SERVER_BIN := $(BINARY_DIR)/server

.PHONY: all build build-email-worker build-server clean run-email-worker run-server

all: build

$(BINARY_DIR):
	mkdir -p $(BINARY_DIR)

build: build-email-worker build-server

build-email-worker: $(BINARY_DIR)
	go build -o $(EMAIL_WORKER_BIN) ./cmd/email_worker

build-server: $(BINARY_DIR)
	go build -o $(SERVER_BIN) ./cmd/server

run-email-worker: build-email-worker
	$(EMAIL_WORKER_BIN)

run-server: build-server
	$(SERVER_BIN)

clean:
	rm -rf $(BINARY_DIR)

.PHONY: test cover cover-html

test:
	go test ./...

cover:
	./scripts/check-coverage.sh

cover-html:
	go test -covermode=set -coverprofile=coverage.out ./internal/... ./utils
	go tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"
