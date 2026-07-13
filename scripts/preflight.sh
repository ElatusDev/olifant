#!/bin/sh
# Pre-work validation for olifant (olifant#61 0b): confirm the live external
# seams are reachable, then run the integration suite against them. Run this
# when you sit down to work — it replaces the never-scheduled self-hosted
# nightly (the mini isn't reliably awake at cron time; local-on-demand runs
# when it's actually useful and needs no runner).
#
# Endpoints resolve the same way the binary does: OLIFANT_* env overrides,
# else the tailnet/laptop defaults. Each integration test t.Skip()s when its
# own dependency is down, so a partial stack yields "skipped", not failure —
# this script's reachability table tells you WHICH seam is down and why a
# test skipped.
set -u

OLLAMA_URL="${OLIFANT_OLLAMA_URL:-http://100.94.233.106:11434}"
CHROMA_URL="${OLIFANT_CHROMA_URL:-http://localhost:8000}"

probe() { # name url path
  if curl -sf -m 4 "$2$3" >/dev/null 2>&1; then
    printf '  %-10s UP    %s\n' "$1" "$2"; return 0
  fi
  printf '  %-10s DOWN  %s\n' "$1" "$2"; return 1
}

echo "olifant preflight — live-stack reachability:"
up=0; total=3
probe "ollama"  "$OLLAMA_URL" "/api/tags"        && up=$((up+1)) || true
probe "chroma"  "$CHROMA_URL" "/api/v2/heartbeat" && up=$((up+1)) || true
if command -v claude >/dev/null 2>&1; then
  printf '  %-10s UP    %s\n' "claude" "$(command -v claude)"; up=$((up+1))
else
  printf '  %-10s DOWN  (claude CLI not on PATH)\n' "claude"
fi

echo
if [ "$up" -eq 0 ]; then
  echo "no seams reachable — every integration test would skip. Bring the stack up:"
  echo "  Tailscale on; kubectl -n platform scale deploy/chromadb --replicas=1;"
  echo "  kubectl -n platform port-forward deploy/chromadb 8000:8000"
  exit 1
fi
echo "$up/$total seams up — running the integration suite (tests skip on down deps)…"
echo
GO="${GO:-/opt/homebrew/bin/go}"
exec "$GO" test -tags=integration -count=1 -p 1 ./...
