#!/bin/sh
# Nightly eval-gate drift backstop (#16 E3, D-EG5). Runs `olifant eval gate
# --notify` against a clean origin/main checkout in a dedicated worktree
# under platform/worktrees/ (kb-root discovery walks up from cwd) — never
# the dev working tree.
#
# Installed to ~/.olifant/eval-gate/nightly.sh (launchd TCC denies executing
# scripts on removable volumes). Accessing the platform volume from launchd
# additionally requires a one-time Full Disk Access grant (E3 finding).
# Environment failures self-report to drift.log as SKIPPED (env) so a silent
# night is distinguishable from a healthy one (IA3).
set -u
REPO="${OLIFANT_REPO:-/Volumes/elatusdev/platform/olifant}"
WT="$(dirname "$REPO")/worktrees/olifant-eval-gate-nightly"
DRIFT="$HOME/.olifant/eval-gate/drift.log"

skip() {
    mkdir -p "$(dirname "$DRIFT")"
    printf '%s SKIPPED (env): %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" >> "$DRIFT"
    exit 0
}

cd "$REPO" 2>/dev/null || skip "platform volume unreachable (unmounted, or launchd lacks Full Disk Access)"
git fetch origin main --quiet 2>/dev/null || skip "git fetch failed (network or TCC)"
if [ ! -d "$WT" ]; then
    git worktree add --detach "$WT" origin/main >/dev/null 2>&1 || skip "worktree add failed"
fi
git -C "$WT" checkout --detach origin/main --quiet 2>/dev/null || skip "worktree checkout failed"
cd "$WT" || skip "worktree cd failed"
make build >/dev/null 2>&1 || skip "build failed on origin/main"
exec ./bin/olifant eval gate --notify
