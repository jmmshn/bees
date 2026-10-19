#!/usr/bin/env bash
# setup.sh [<dir holding a bs build>]: a throwaway project for a live test of bs, with its
# queen running that build in its own herdr workspace. Prints the run's live.json.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
bin=$(cd "${1:-$here/../../../bin}" 2>/dev/null && pwd) || bin=
[ -n "$bin" ] && [ -x "$bin/bs" ] || { echo "no bs build in ${1:-bin/}: run go build -o bin/bs ./cmd/bs in the checkout under test" >&2; exit 1; }
# The project name is the toy's id everywhere: its beads (<toy>-N), logbook (<toy>.db),
# cache files (<toy>-*) and sessions (queen-<toy>, <toy>-N). teardown.sh refuses any other shape.
toy=lt$(date -u +%y%m%d%H%M%S)
# Outside any repo: Claude walks up for CLAUDE.md, and bees' would load into the toy's sessions.
root=${BEES_LIVE:-$HOME/.cache/bees-live}
run=$root/$toy repo=$root/$toy/$toy
[ -e "$run" ] && { echo "exists: $run" >&2; exit 1; }
mkdir -p "$run/keeper"
git init -q -b main "$repo"
git -C "$repo" config commit.gpgsign false # the signing key and identity live under ~/repos only
git -C "$repo" config user.name live-test && git -C "$repo" config user.email live-test@example.invalid
printf '# %s\n\nA throwaway project for a live test of bs.\n' "$toy" >"$repo/README.md"
git -C "$repo" add -A && git -C "$repo" commit -qm "toy"
ws=$(herdr workspace create --cwd "$repo" --label "live $toy" --no-focus)
pane=$(jq -r .result.root_pane.pane_id <<<"$ws")
jq -n --arg toy "$toy" --arg run "$run" --arg repo "$repo" --arg pane "$pane" --arg bin "$bin" \
	--arg ws "$(jq -r .result.workspace.workspace_id <<<"$ws")" --arg t0 "$(date -u +%FT%TZ)" \
	--arg build "$("$bin/bs" version 2>&1)" --arg shared "$(command -v bs >/dev/null && bs version 2>&1 || echo none)" \
	'{toy:$toy,run:$run,repo:$repo,pane:$pane,workspace:$ws,bin:$bin,created:$t0,queen_bs:$build,worker_bs:$shared}' >"$run/live.json"
# bs queen drops BS_PROJECT and finds the toy from the cwd. The PATH reaches the queen's own
# bs calls only: workers start from herdr's environment and run the shared bs.
herdr pane run "$pane" "PATH=$bin:\$PATH bs queen" >/dev/null
cat "$run/live.json"
# Trust the new folder once, as a human would; Claude keys trust by repo root, so workers' worktrees inherit it.
for _ in $(seq 30); do
	herdr pane read "$pane" 2>/dev/null | grep -q "Yes, I trust this folder" && { herdr pane send-keys "$pane" down enter >/dev/null; break; }
	sleep 1
done
