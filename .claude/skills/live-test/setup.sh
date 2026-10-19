#!/usr/bin/env bash
# setup.sh [<dir holding a bs build>]: a throwaway project for a live test of bs, with its
# queen running that build in its own herdr workspace. Prints the run's live.json.
set -euo pipefail
unset BS_LOGBOOK BS_PROJECT # the toy's sessions find their logbook and project from its name
here=$(cd "$(dirname "$0")" && pwd)
bin=$(cd "${1:-$here/../../../bin}" 2>/dev/null && pwd) || bin=
[ -n "$bin" ] && [ -x "$bin/bs" ] || { echo "no bs build in ${1:-bin/}: run go build -o bin/bs ./cmd/bs in the checkout under test" >&2; exit 1; }
# The project name is the toy's id everywhere: its beads (<toy>-N), logbook (<toy>.db),
# cache files (<toy>-*) and sessions (queen-<toy>, <toy>-N). teardown.sh refuses any other shape.
toy=lt$(date -u +%y%m%d%H%M%S)$(printf %04d $((RANDOM % 10000)))
# Outside any repo: Claude walks up for CLAUDE.md, and bees' would load into the toy's sessions.
root=${BEES_LIVE:-$HOME/.cache/bees-live}
# pwd -P: herdr reports pane cwds with symlinks resolved, and teardown.sh matches on them.
mkdir -p "$root" && root=$(cd "$root" && pwd -P)
run=$root/$toy repo=$root/$toy/$toy
mkdir "$run" && mkdir "$run/keeper" # mkdir fails on a clash
# live.json is written first and grows with each step, so teardown.sh can undo a half-built setup.
jq -n --arg toy "$toy" --arg run "$run" --arg repo "$repo" --arg bin "$bin" --arg t0 "$(date -u +%FT%TZ)" \
	--arg build "$("$bin/bs" version 2>/dev/null)" --arg shared "$(cd / && bs version 2>/dev/null || echo none)" \
	'{toy:$toy,run:$run,repo:$repo,bin:$bin,created:$t0,queen_bs:$build,worker_bs:$shared}' >"$run/live.json"
record() { jq --arg k "$1" --arg v "$2" '.[$k] = $v' "$run/live.json" >"$run/live.json.new" && mv "$run/live.json.new" "$run/live.json"; }
git init -q -b main "$repo"
git -C "$repo" config commit.gpgsign false # the signing key and identity live under ~/repos only
git -C "$repo" config user.name live-test && git -C "$repo" config user.email live-test@example.invalid
printf '# %s\n\nA throwaway project for a live test of bs.\n' "$toy" >"$repo/README.md"
git -C "$repo" add -A && git -C "$repo" commit -qm "toy"
ws=$(herdr workspace create --cwd "$repo" --label "live $toy" --no-focus)
record workspace "$(jq -r .result.workspace.workspace_id <<<"$ws")"
pane=$(jq -r .result.root_pane.pane_id <<<"$ws")
record pane "$pane"
# bs queen drops BS_PROJECT and finds the toy from the cwd. The PATH reaches the queen's own
# bs calls only: workers start from herdr's environment and run the shared bs.
herdr pane run "$pane" "PATH=$bin:\$PATH bs queen" >/dev/null
cat "$run/live.json"
# Trust the new folder once, as a human would; Claude keys trust by repo root, so workers' worktrees
# inherit it. The prompt can draw before Claude reads keys (bees-8.1), so send them until it's gone.
seen=
for _ in $(seq 30); do
	if herdr pane read "$pane" 2>/dev/null | grep -q "Yes, I trust this folder"; then
		seen=1
		herdr pane send-keys "$pane" down enter >/dev/null
	elif [ -n "$seen" ]; then
		exit 0
	fi
	sleep 2 # time for Claude to take the keys and redraw
done
[ -z "$seen" ] || { echo "setup: the folder-trust prompt is still up in $pane; answer it there" >&2; exit 1; }
