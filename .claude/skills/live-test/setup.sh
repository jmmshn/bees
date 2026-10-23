#!/usr/bin/env bash
# setup.sh [--no-queen] [--harness agy] [--cl] [<dir holding a bs build>]: a throwaway project for a
# live test, on its own herdr --session server, with its session config dir written and (unless
# --no-queen) its queen running that build. Prints the run's live.json.
# --harness agy: the queen stays on Claude Code and spawns agy workers (bs spawn --harness agy);
# setup checks agy is signed in from the toy's panes first. AGY_MODEL sets [worker.agy] model,
# AGY_SETTINGS agy's settings.json (default ~/.gemini/antigravity-cli/settings.json).
# LIVE_SSH=1 starts the toy's herdr server as one started over ssh is, its panes handed
# SSH_CONNECTION, SSH_CLIENT and SSH_TTY (bees-13.13.13).
# --cl: the toy is CL-shaped (bees-14.6): cltoy/cl, a toy monorepo client and CL tool, is the
# command workspace ([workspace.command]) and the command review host ([review.command]), on
# every toy pane's PATH; config.toml says how, and cl-host.md is the host's prompt section.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
queen=1 harness=claude_code cl=0
while :; do
	case ${1:-} in
	--no-queen) queen=0 && shift ;;
	--harness) [ "${2:-}" = agy ] || { echo "setup: --harness takes agy (claude_code is the default)" >&2; exit 1; }; harness=agy && shift 2 ;;
	--cl) cl=1 && shift ;;
	*) break ;;
	esac
done
bin=$(cd "${1:-$here/../../../bin}" 2>/dev/null && pwd) || bin=
[ -n "$bin" ] && [ -x "$bin/bs" ] || { echo "no bs build in ${1:-bin/}: run go build -o bin/bs ./cmd/bs in the checkout under test" >&2; exit 1; }
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
# bs reads a pane's config from here when its HERDR_SESSION isn't default (ADR-0010).
config=${XDG_CONFIG_HOME:-$HOME/.config}/bees/sessions/$toy
mkdir "$run" && mkdir "$run/keeper" "$run/logbooks" # mkdir fails on a clash
h() { herdr --session "$toy" "$@"; }
# The keeper's own workspaces, so teardown can show it left them alone.
keeper_ws=$(herdr workspace list 2>/dev/null | jq -c '[.result.workspaces[].workspace_id]' || echo null)
# live.json is written first and grows with each step, so teardown.sh can undo a half-built setup.
jq -n --arg toy "$toy" --arg run "$run" --arg repo "$repo" --arg bin "$bin" --arg config "$config" --arg t0 "$(date -u +%FT%TZ)" \
	--arg build "$("$bin/bs" version 2>/dev/null || echo unknown)" \
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
# panes (bees-13.1's probe saw the launcher's leak). XDG is left as the keeper has it, set
# or not, so the toy's panes read the config dir written above and herdr's socket paths
# work as they do for the keeper.
xdg=()
for k in XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME XDG_STATE_HOME XDG_RUNTIME_DIR; do
	[ -z "${!k:-}" ] || xdg+=("$k=${!k}")
done
# setsid keeps the server out of this shell's session; stock macOS has none, and nohup
# at least keeps it past this shell's hangup. ${a[@]+...}: bash 3.2 calls an empty array
# unset under set -u.
detach=setsid
command -v setsid >/dev/null || detach=nohup
clbin=
if [ $cl = 1 ]; then
	# Named after the toy, so teardown.sh removes it: the tool and its state, and the clients.
	clbin=$run/$toy-cl/bin
	mkdir -p "$clbin" && cp "$here/cltoy/cl" "$clbin/cl" && "$clbin/cl" init "$repo"
	record cl "$clbin/cl"
	sed "s|{{cl}}|$clbin/cl|g" "$here/cltoy/cl-host.md" >"$config/cl-host.md"
	sed -e "s|{{clients}}|$run/$toy-clients|g" -e "s|{{config}}|$config|g" "$here/cltoy/config.toml" >>"$config/config.toml"
	clbin=$clbin:
fi
toyenv=(env -i PATH="$bin:$clbin$PATH" HOME="$HOME" TERM="${TERM:-xterm-256color}" LANG="${LANG:-C.UTF-8}" SHELL="${SHELL:-/bin/sh}" ${xdg[@]+"${xdg[@]}"})
if [ $harness = agy ]; then
	record harness agy
	# teardown.sh removes the toy's trust entries and agy files from here, and checks it
	# against its own AGY_SETTINGS as it does the config dir.
	record agy_settings "${AGY_SETTINGS:-$HOME/.gemini/antigravity-cli/settings.json}"
	[ -z "${AGY_MODEL:-}" ] || printf '\n[worker.agy]\nmodel = "%s"\n' "$AGY_MODEL" >>"$config/config.toml"
	# Signed in as the toy's panes will be: agy reads SSH_CONNECTION/SSH_CLIENT as a remote
	# session and is "not signed in" there, which the bare env drops (bees-13.13.8).
	agy=$("${toyenv[@]}" sh -c 'command -v agy') || { echo "setup: no agy on the toy's PATH" >&2; exit 1; }
	record agy_version "$("$agy" --version 2>/dev/null || echo unknown)" # agy updates itself (motif 14)
	out=$(cd "$run/keeper" && "${toyenv[@]}" timeout 60 "$agy" models 2>&1) || true
	grep -qi 'sign in' <<<"$out" && { echo "setup: agy isn't signed in from the toy's env; run agy once to sign in: $out" >&2; exit 1; }
	jq -e '.permissions.allow // [] | index("command(bs)")' "$(jq -r .agy_settings "$run/live.json")" >/dev/null 2>&1 ||
		echo "setup: agy's permissions.allow has no command(bs): the agy worker's pane asks the keeper before every bs call" >&2
fi
# The ( … & ) double fork reparents the server off this script at once: setsid in a
# script's background job doesn't fork, so the server stayed this script's child, and a
# worker running setup was the toy queen's nearest recorded ancestor (bees-13.13.8).
serverenv=("${toyenv[@]}")
[ "${LIVE_SSH:-}" != 1 ] || { serverenv+=("SSH_CONNECTION=100.64.0.1 50090 100.64.0.2 22" "SSH_CLIENT=100.64.0.1 50090 22" "SSH_TTY=/dev/pts/0") && record live_ssh 1; }
("${serverenv[@]}" \
	"$detach" herdr --session "$toy" server </dev/null >"$run/keeper/herdr-server.log" 2>&1 &)
for _ in $(seq 50); do h workspace list >/dev/null 2>&1 && break; sleep 0.2; done
h workspace list >/dev/null || { echo "setup: herdr --session $toy never answered; see $run/keeper/herdr-server.log" >&2; exit 1; }
ws=$(h workspace create --cwd "$repo" --label "live $toy" --no-focus)
record workspace "$(jq -r .result.workspace.workspace_id <<<"$ws")"
pane=$(jq -r .result.root_pane.pane_id <<<"$ws")
record pane "$pane"
# Which bs the pane's shell runs: its rc may put dirs ahead of the build (motif 14).
h pane run "$pane" "{ command -v bs || echo none; } >$(printf %q "$run/keeper/pane-which")" >/dev/null
for _ in $(seq 50); do [ -s "$run/keeper/pane-which" ] && break; sleep 0.2; done
# "unknown", never "": the pane's shell didn't answer in 10 s (motif 11).
record pane_bs "$(sed -n 1p "$run/keeper/pane-which" 2>/dev/null | grep . || echo unknown)"
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
