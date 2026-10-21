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
# bd records git user.name as a bead's created_by: the toy id marks every bead the toy's
# sessions file, wherever they file it, so teardown.sh can find and delete it.
git -C "$repo" config user.name "$toy" && git -C "$repo" config user.email live-test@example.invalid
printf '# %s\n\nA throwaway project for a live test of bs.\n' "$toy" >"$repo/README.md"
git -C "$repo" add -A && git -C "$repo" commit -qm "toy"
ws=$(herdr workspace create --cwd "$repo" --label "live $toy" --no-focus)
record workspace "$(jq -r .result.workspace.workspace_id <<<"$ws")"
pane=$(jq -r .result.root_pane.pane_id <<<"$ws")
record pane "$pane"
# The pane's shell starts from herdr's environment, not this one, so unset BS_LOGBOOK and
# BS_PROJECT there too: the queen finds the toy from the cwd. The PATH reaches the queen's own
# bs calls only: workers start from herdr's environment and run the shared bs, unless the
# queen spawns them with --path "$bin".
herdr pane run "$pane" "env -u BS_LOGBOOK -u BS_PROJECT PATH=$(printf %q "$bin"):\$PATH bs queen" >/dev/null
cat "$run/live.json"
# Trust the new folder once, as a human would; Claude keys trust by repo root, so workers'
# worktrees inherit it. The prompt can draw before Claude reads keys (bees-8.1): send them
# once, give Claude 10 s to take them, and only then send again. Sending while the prompt
# is merely slow to redraw would put an extra enter into the queen's input box.
trusting() { herdr pane read "$pane" 2>/dev/null | grep -q "Yes, I trust this folder"; }
for _ in $(seq 30); do trusting && break; sleep 1; done
for _ in 1 2 3; do
	trusting || break
	herdr pane send-keys "$pane" down enter >/dev/null
	for _ in $(seq 10); do sleep 1; trusting || break; done
done
trusting && { echo "setup: the folder-trust prompt is still up in $pane; answer it there" >&2; exit 1; }
# Setup succeeds only with the queen's Claude up and idle (motif 11): a bs queen that failed,
# or a Claude that never drew, leaves no agent to wait for.
herdr agent wait "$pane" --until idle --timeout 60000 >/dev/null || { echo "setup: no queen came up in $pane; read it with herdr pane read $pane" >&2; exit 1; }
