BIN ?= bin/olifant
GO  ?= /opt/homebrew/bin/go

.PHONY: all build tidy clean run fmt vet test corpus hooks nightly-install nightly-remove

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

# Install the repo's git hooks (eval regression gate pre-push, #16)
hooks:
	git config core.hooksPath scripts/hooks

# Nightly eval-gate drift backstop (#16 E3): install/remove the launchd job
# The runner script is copied to the internal disk: launchd's TCC denies
# executing scripts that live on removable volumes (E3 finding).
nightly-install:
	mkdir -p ~/.olifant/eval-gate
	cp scripts/launchd/eval-gate-nightly.sh ~/.olifant/eval-gate/nightly.sh
	sed "s|__HOME__|$$HOME|" scripts/launchd/com.elatusdev.olifant.eval-gate.plist > ~/Library/LaunchAgents/com.elatusdev.olifant.eval-gate.plist
	launchctl bootstrap gui/$$(id -u) ~/Library/LaunchAgents/com.elatusdev.olifant.eval-gate.plist

nightly-remove:
	launchctl bootout gui/$$(id -u)/com.elatusdev.olifant.eval-gate || true
	rm -f ~/Library/LaunchAgents/com.elatusdev.olifant.eval-gate.plist
