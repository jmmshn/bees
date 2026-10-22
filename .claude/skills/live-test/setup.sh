#!/usr/bin/env bash
# setup.sh [--no-queen] [<dir holding a bs or bsx build>]: a throwaway project for a live
# test, on its own herdr --session server, with its session config dir written and (unless
# --no-queen, or the build has no bs) its queen running that build. Prints the run's live.json.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
queen=1
[ "${1:-}" = --no-queen ] && { queen=0; shift; }
bin=$(cd "${1:-$here/../../../bin}" 2>/dev/null && pwd) || bin=
[ -n "$bin" ] && { [ -x "$bin/bs" ] || [ -x "$bin/bsx" ]; } || { echo "no bs or bsx build in ${1:-bin/}: run go build -o bin/bsx ./cmd/bsx (or bin/bs ./cmd/bs) in the checkout under test" >&2; exit 1; }
[ -x "$bin/bs" ] || queen=0 # bsx has no queen yet (bees-13.4)
[[ $HOME == /* ]] || { echo "HOME=$HOME is not absolute" >&2; exit 1; }
# The project name is the toy's id everywhere: its herdr session, config dir, beads (<toy>-N),
# logbook (<toy>.db), cache files (<toy>-*) and sessions (queen-<toy>, <toy>-N).
# teardown.sh refuses any other shape.
toy=lt$(date -u +%y%m%d%H%M%S)$(printf %04d $((RANDOM % 10000)))
# Outside any repo: Claude walks up for CLAUDE.md, and bees' would load into the toy's sessions.
root=${BEES_LIVE:-$HOME/.cache/bees-live}
# pwd -P: herdr reports pane cwds with symlinks resolved, and teardown.sh matches on them.
mkdir -p "$root" && root=$(cd "$root" && pwd -P)
run=$root/$toy repo=$root/$toy/$toy
# bsx reads a pane's config from here when its HERDR_SESSION isn't default (ADR-0010).
config=${XDG_CONFIG_HOME:-$HOME/.config}/bees/sessions/$toy
mkdir "$run" && mkdir "$run/keeper" "$run/logbooks" # mkdir fails on a clash
h() { herdr --session "$toy" "$@"; }
# The keeper's own workspaces, so teardown can show it left them alone.
keeper_ws=$(herdr workspace list 2>/dev/null | jq -c '[.result.workspaces[].workspace_id]' || echo null)
# live.json is written first and grows with each step, so teardown.sh can undo a half-built setup.
jq -n --arg toy "$toy" --arg run "$run" --arg repo "$repo" --arg bin "$bin" --arg config "$config" --arg t0 "$(date -u +%FT%TZ)" \
	--arg build "$({ "$bin/bsx" version || "$bin/bs" version; } 2>/dev/null || echo "$(git -C "$bin" rev-parse --short HEAD 2>/dev/null || echo unknown) (bsx: no version command yet)")" \
	--arg shared "$(cd / && bs version 2>/dev/null || echo none)" --argjson keeper "$keeper_ws" \
	'{toy:$toy,session:$toy,run:$run,repo:$repo,bin:$bin,config:$config,created:$t0,build:$build,shared_bs:$shared,keeper_workspaces:$keeper}' >"$run/live.json"
record() { jq --arg k "$1" --arg v "$2" '.[$k] = $v' "$run/live.json" >"$run/live.json.new" && mv "$run/live.json.new" "$run/live.json"; }
git init -q -b main "$repo"
git -C "$repo" config commit.gpgsign false # the signing key and identity live under ~/repos only
# bd records git user.name as a bead's created_by: the toy id marks every bead the toy's
# sessions file, wherever they file it, so teardown.sh can find and delete it.
git -C "$repo" config user.name "$toy" && git -C "$repo" config user.email live-test@example.invalid
printf '# %s\n\nA throwaway project for a live test of bs.\n' "$toy" >"$repo/README.md"
git -C "$repo" add -A && git -C "$repo" commit -qm "toy"
# The toy's config: its own logbook dir in the run dir, never the real one. Edit it (or add
# projects/<toy>.toml) before the check; teardown.sh removes the whole dir.
mkdir -p "$config"
printf '[paths]\nlogbook_dir = "%s"\n' "$run/logbooks" >"$config/config.toml"
# The toy's own herdr server, from a bare env: no BS_* or HERDR_* of this shell reaches its
# panes (bees-13.1's probe saw the launcher's leak). XDG stays unset, so the keeper's
# shell rc and herdr's socket paths work as they do for the keeper.
env -i PATH="$bin:$PATH" HOME="$HOME" TERM="${TERM:-xterm-256color}" LANG="${LANG:-C.UTF-8}" SHELL="${SHELL:-/bin/sh}" \
	setsid herdr --session "$toy" server </dev/null >"$run/keeper/herdr-server.log" 2>&1 &
for _ in $(seq 50); do h workspace list >/dev/null 2>&1 && break; sleep 0.2; done
h workspace list >/dev/null || { echo "setup: herdr --session $toy never answered; see $run/keeper/herdr-server.log" >&2; exit 1; }
ws=$(h workspace create --cwd "$repo" --label "live $toy" --no-focus)
record workspace "$(jq -r .result.workspace.workspace_id <<<"$ws")"
pane=$(jq -r .result.root_pane.pane_id <<<"$ws")
record pane "$pane"
# Which bs and bsx the pane's shell runs: its rc may put dirs ahead of the build (motif 14).
h pane run "$pane" "{ command -v bsx || echo none; command -v bs || echo none; } >$(printf %q "$run/keeper/pane-which")" >/dev/null
for _ in $(seq 50); do [ -s "$run/keeper/pane-which" ] && break; sleep 0.2; done
record pane_bsx "$(sed -n 1p "$run/keeper/pane-which" 2>/dev/null)"
record pane_bs "$(sed -n 2p "$run/keeper/pane-which" 2>/dev/null)"
if [ $queen = 0 ]; then
	cat "$run/live.json"
	exit 0
fi
# The PATH prefix reaches the queen's own bs calls; workers start from the toy server's env,
# where the pane's shell rc decides (pane_bs).
h pane run "$pane" "PATH=$(printf %q "$bin"):\$PATH bs queen" >/dev/null
cat "$run/live.json"
# Trust the new folder once, as a human would; Claude keys trust by repo root, so workers'
# worktrees inherit it. The prompt can draw before Claude reads keys (bees-8.1): send them
# once, give Claude 10 s to take them, and only then send again. Sending while the prompt
# is merely slow to redraw would put an extra enter into the queen's input box.
trusting() { h pane read "$pane" 2>/dev/null | grep -q "Yes, I trust this folder"; }
for _ in $(seq 30); do trusting && break; sleep 1; done
for _ in 1 2 3; do
	trusting || break
	h pane send-keys "$pane" down enter >/dev/null
	for _ in $(seq 10); do sleep 1; trusting || break; done
done
trusting && { echo "setup: the folder-trust prompt is still up in $pane; answer it there" >&2; exit 1; }
# Setup succeeds only with the queen's Claude up and idle (motif 11): a bs queen that failed,
# or a Claude that never drew, leaves no agent to wait for.
h agent wait "$pane" --until idle --timeout 60000 >/dev/null || { echo "setup: no queen came up in $pane; read it with herdr --session $toy pane read $pane" >&2; exit 1; }
