#!/bin/sh
# Daily eval-gate drift backstop (#16 E3, D-EG5). Runs `olifant eval gate
# --notify` against a clean origin/main checkout in a dedicated worktree
# under platform/worktrees/ (kb-root discovery walks up from cwd) — never
# the dev working tree.
#
# Scheduled at 08:00 (was 02:30): the machine and stack (colima chroma
# port-forward, tailnet ollama) are far more likely up at the start of the
# workday, and the ~30-min run proceeds in parallel with normal work. The
# file/label keep the historical "nightly" name (rename churn > value).
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
#
# This worktree is PROTECTED, not legacy (olifant#95 GR-F1): `eval gate
# -git-ref` exists for operator mints, but the nightly cannot use it —
# (a) repo/history/corpus sync WRITE manifests here and the auto-PR
# commits+pushes from this tree; (b) the gate below must fingerprint the
# FRESHLY-SYNCED manifests before that PR lands — a ref read would
# fingerprint stale pre-sync blobs and silently break receipt semantics.
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

# Family freshness composition (olifant#82 D-FF7, extending olifant#77
# D-CS4/D-CS5): reconcile EVERY family the gate's retrieval substrate
# queries BEFORE receipts mint — repo sync (code_*), history index
# (history_*/code_history_*, cursor advanced by the index itself),
# failure-modes on-change, corpus sync — then land all changed state files
# in ONE auto-PR, then gate. Each step self-reports; a failed step never
# silently skips the gate.
note() {
    mkdir -p "$(dirname "$DRIFT")"
    printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" >> "$DRIFT"
}

# 0) §7 auto-demotion trigger (olifant#93 D-DT5): reconcile the promotion
#    ledger against confirmed false-block reaction labels. Offline and
#    sub-second; no -kb-root (the pinned worktree carries no untracked turn
#    records, so the verdict map is the honest evidence source here). A
#    failure never skips the gate.
rcout=$(./bin/olifant promote reconcile 2>&1) || note "RECONCILE FAILED: $(echo "$rcout" | tail -1)"
echo "$rcout" | grep -q "^demoted " && note "RECONCILE: $(echo "$rcout" | grep '^demoted ' | tr '\n' ';')"

# 1) code_* — manifest-diff incremental (olifant#82 D-FF2).
rsout=$(./bin/olifant repo sync -kb-root "$KBWT" 2>&1) || note "REPO SYNC FAILED: $(echo "$rsout" | tail -1)"
echo "$rsout" | grep -q "^repo sync synced" && note "REPO SYNC: $(echo "$rsout" | tail -1)"

# 2) history_* / code_history_* — incremental from the cursor; the index
#    advances the cursor itself (scan-then-index would starve the index).
hiout=$(./bin/olifant history index -update-manifest -kb-root "$KBWT" -platform-root "$(dirname "$REPO")" 2>&1) \
    || note "HISTORY INDEX FAILED: $(echo "$hiout" | tail -1)"
echo "$hiout" | grep -q "manifest advanced" && note "HISTORY: $(echo "$hiout" | grep 'commit chunks upserted' | tail -1 | tr -s ' ')"

# 3) failure_modes_* — re-index only when the curated source changed
#    (olifant#82 D-FF5). Drop-recreate is fine at this family's size; the
#    marker advances only after a successful index, so a failed night
#    retries the next.
FMSRC=$(ls "$KBWT"/eval/failure-modes/v*.yaml 2>/dev/null | sort | tail -1)
FMMARK="$HOME/.olifant/eval-gate/fm-last-sha"
if [ -n "$FMSRC" ]; then
    fmsha=$(shasum -a 256 "$FMSRC" | cut -d' ' -f1)
    if [ "$fmsha" != "$(cat "$FMMARK" 2>/dev/null)" ]; then
        for sc in universal backend webapp mobile e2e infra platform_process; do
            curl -s -o /dev/null -X DELETE "http://localhost:8000/api/v2/tenants/default_tenant/databases/default_database/collections/failure_modes_$sc" || true
        done
        if fmout=$(./bin/olifant dataset index -kb-root "$KBWT" 2>&1); then
            printf '%s' "$fmsha" > "$FMMARK"
            note "FAILURE-MODES REINDEXED: $(basename "$FMSRC")"
        else
            note "FAILURE-MODES INDEX FAILED (collections dropped, retry next night): $(echo "$fmout" | tail -1)"
        fi
    fi
fi

# 4) corpus_* — incremental (olifant#77 D-CS5), unchanged.
syncout=$(./bin/olifant corpus sync -kb-root "$KBWT" 2>&1) || note "SYNC FAILED: $(echo "$syncout" | tail -1)"
echo "$syncout" | grep -q "corpus sync synced" && note "SYNC: $(echo "$syncout" | grep 'corpus sync' | tail -1)"

# 5) Land every changed state file in ONE auto-PR (corpus manifest,
#    repo manifest, history cursor) — receipts fingerprint the pinned
#    tree's freshly-written manifests, so a failed PR only lags main.
if ! git -C "$KBWT" diff --quiet -- corpus/v1/manifest.yaml corpus/v1/repo-manifest.yaml short-term/history-manifest.yaml 2>/dev/null \
   || [ -n "$(git -C "$KBWT" ls-files --others --exclude-standard -- corpus/v1/repo-manifest.yaml)" ]; then
    BR="ops/family-sync-nightly"
    if pr_err=$( (git -C "$KBWT" checkout -B "$BR" \
        && git -C "$KBWT" add corpus/v1/manifest.yaml corpus/v1/repo-manifest.yaml short-term/history-manifest.yaml \
        && git -C "$KBWT" commit -m "ops(corpus): nightly family sync manifests" \
        && git -C "$KBWT" push -f origin "$BR" \
        && gh pr create --repo ElatusDev/platform-knowledge-base --head "$BR" \
             --title "ops(corpus): nightly family sync manifests" \
             --body "Automated nightly family sync (olifant#77 D-CS5 + olifant#82 D-FF7)." 2>&1 \
        ; for i in 1 2 3 4 5 6; do
            m=$(gh pr view "$BR" --repo ElatusDev/platform-knowledge-base --json mergeable -q .mergeable 2>/dev/null)
            [ "$m" = "MERGEABLE" ] && break; sleep 10
          done \
        && gh pr merge "$BR" --repo ElatusDev/platform-knowledge-base --squash --delete-branch) 2>&1 ); then
        note "MANIFESTS LANDED (auto-PR merged)"
    else
        note "MANIFEST PR FAILED (index ahead of main until next sync): $(echo "$pr_err" | tail -1)"
    fi
    git -C "$KBWT" checkout --detach HEAD --quiet 2>/dev/null || true
fi

exec ./bin/olifant eval gate --notify -kb-root "$KBWT"
