BIN ?= bin/olifant
GO  ?= /opt/homebrew/bin/go

.PHONY: all build tidy clean run fmt vet test corpus

all: build

build:
	$(GO) build -o $(BIN) .

tidy:
	$(GO) mod tidy

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

clean:
	rm -rf bin/ ../knowledge-base/corpus/v1/*.ndjson ../knowledge-base/corpus/v1/manifest.yaml

# Convenience: build + run the corpus builder with verbose output
corpus: build
	$(BIN) corpus build -v
