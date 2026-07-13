BIN ?= bin/olifant
GO  ?= /opt/homebrew/bin/go

.PHONY: all build tidy clean run fmt vet test test-integration preflight corpus hooks nightly-install nightly-remove

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

# Integration tests against the LIVE seams (Ollama, ChromaDB, claude CLI).
# Gated behind the `integration` build tag so they never run in default CI.
# Tests t.Skip individually when a dependency is unreachable, so this is safe
# to run with the stack partly down. Needs: tailnet Ollama + a ChromaDB
# port-forward (kubectl -n platform port-forward deploy/chromadb 8000:8000).
# -p 1 serializes packages: Ollama runs one generation at a time (~10 tok/s on
# the mini), so parallel synth-heavy packages (challenge/eval/validate/cmd)
# queue behind each other and blow their per-test deadlines.
test-integration:
	$(GO) test -tags=integration -count=1 -v -p 1 ./...

# Pre-work validation (olifant#61 0b): probe the live seams, then run the
# integration suite against whatever is up. Replaces the never-scheduled
# self-hosted nightly — the mini isn't reliably awake at cron time, and a
# local on-demand check needs no runner and runs when it is actually useful.
preflight:
	sh scripts/preflight.sh

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
