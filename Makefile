BINARY ?= outwright-imap-push
BIN_DIR ?= bin
IMAGE ?= outwright-imap-push

.PHONY: build test ci docker-build

build:
	CGO_ENABLED=0 go build -o $(BIN_DIR)/$(BINARY) ./cmd/outwright-imap-push

test:
	go test ./...

# ci fails if gofmt would change anything, then vets and tests.
ci:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "gofmt needed"; exit 1)
	go vet ./...
	go test ./...

docker-build:
	docker build -t $(IMAGE) .
