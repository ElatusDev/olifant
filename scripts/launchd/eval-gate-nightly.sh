#!/bin/sh
# Nightly eval-gate drift backstop (#16 E3, D-EG5). Runs `olifant eval gate
# --notify` against a clean origin/main checkout in a dedicated worktree
# under platform/worktrees/ (kb-root discovery walks up from cwd) — never
# the dev working tree.
#
# Installed to ~/.olifant/eval-gate/nightly.sh (launchd TCC denies executing
# scripts on removable volumes). Accessing the platform volume from launchd
# additionally requires a one-time Full Disk Access grant to /bin/sh in
# System Settings → Privacy & Security (E3 finding, expanded post-merge).
#
# launchd's restricted environment surfaces two more gotchas that this
# script handles inline:
#   - Stripped PATH: launchd default PATH lacks /opt/homebrew/bin, so git
#     and other Homebrew tools aren't found. We prepend explicitly here.
#   - Make weirdness: the launchd-context `make build` fails with "No rule
#     to make target `build'" even when an interactive shell in the same
#     worktree succeeds with the same Makefile (root cause unclear; possibly
#     XCode CLT proxy semantics). Bypassed by calling `go build` directly.
#
# Environment failures self-report to drift.log as SKIPPED (env) with the
# captured stderr so a silent night is distinguishable from a healthy one
# (IA3) and the failure mode is identifiable without re-running.
set -u
REPO="${OLIFANT_REPO:-/Volumes/elatusdev/platform/olifant}"
WT="$(dirname "$REPO")/worktrees/olifant-eval-gate-nightly"
# Self-pin the KB tree to origin/main (olifant#74 / D-PG2): findUp from the
# olifant worktree resolves the shared knowledge-base symlink, which a
# concurrent instance may have parked on a feature branch — false-failing the
# drift backstop. A dedicated origin/main KB worktree, passed via -kb-root,
# makes the nightly deterministic w.r.t. main regardless of the symlink.
KBREPO="$(dirname "$REPO")/platform-knowledge-base"
KBWT="$(dirname "$REPO")/worktrees/kb-eval-gate-nightly"
DRIFT="$HOME/.olifant/eval-gate/drift.log"
PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:$PATH
export PATH

skip() {
    mkdir -p "$(dirname "$DRIFT")"
    printf '%s SKIPPED (env): %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" >> "$DRIFT"
    exit 0
}

cd "$REPO" 2>/dev/null || skip "platform volume unreachable (unmounted, or launchd lacks Full Disk Access)"
err=$(git fetch origin main --quiet 2>&1)      || skip "git fetch: $err"
if [ ! -d "$WT" ]; then
    err=$(git worktree add --detach "$WT" origin/main 2>&1) || skip "worktree add: $err"
fi
err=$(git -C "$WT" checkout --detach origin/main --quiet 2>&1) || skip "worktree checkout: $err"

# Refresh the pinned KB worktree to origin/main (same discipline as the olifant one).
err=$(git -C "$KBREPO" fetch origin main --quiet 2>&1) || skip "kb fetch: $err"
if [ ! -d "$KBWT" ]; then
    err=$(git -C "$KBREPO" worktree add --detach "$KBWT" origin/main 2>&1) || skip "kb worktree add: $err"
fi
err=$(git -C "$KBWT" checkout --detach origin/main --quiet 2>&1) || skip "kb worktree checkout: $err"

cd "$WT" || skip "worktree cd failed"
err=$(/opt/homebrew/bin/go build -o bin/olifant . 2>&1) || skip "go build: $(echo "$err" | tail -1)"

# Corpus freshness (olifant#77, D-CS4/D-CS5): sync the index incrementally
# against the pinned tree, land the manifest via auto-PR + self-merge, THEN
# gate — receipts fingerprint the pinned tree's freshly-written manifest, so
# they stay consistent with the indexed state even if the PR step fails
# (self-reported below; the next night's re-diff heals the lag).
note() {
    mkdir -p "$(dirname "$DRIFT")"
    printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" >> "$DRIFT"
}
syncout=$(./bin/olifant corpus sync -kb-root "$KBWT" 2>&1) || note "SYNC FAILED: $(echo "$syncout" | tail -1)"
echo "$syncout" | grep -q "corpus sync synced" && {
    note "SYNC: $(echo "$syncout" | grep 'corpus sync' | tail -1)"
    BR="ops/corpus-sync-nightly"
    if pr_err=$( (git -C "$KBWT" checkout -B "$BR" \
        && git -C "$KBWT" add corpus/v1/manifest.yaml \
        && git -C "$KBWT" commit -m "ops(corpus): nightly incremental sync manifest" \
        && git -C "$KBWT" push -f origin "$BR" \
        && gh pr create --repo ElatusDev/platform-knowledge-base --head "$BR" \
             --title "ops(corpus): nightly incremental sync manifest" \
             --body "Automated nightly corpus sync (olifant#77 D-CS5)." 2>&1 \
        ; for i in 1 2 3 4 5 6; do
            m=$(gh pr view "$BR" --repo ElatusDev/platform-knowledge-base --json mergeable -q .mergeable 2>/dev/null)
            [ "$m" = "MERGEABLE" ] && break; sleep 10
          done \
        && gh pr merge "$BR" --repo ElatusDev/platform-knowledge-base --squash --delete-branch) 2>&1 ); then
        note "MANIFEST LANDED (auto-PR merged)"
    else
        note "MANIFEST PR FAILED (index ahead of main until next sync): $(echo "$pr_err" | tail -1)"
    fi
    git -C "$KBWT" checkout --detach HEAD --quiet 2>/dev/null || true
}

exec ./bin/olifant eval gate --notify -kb-root "$KBWT"
