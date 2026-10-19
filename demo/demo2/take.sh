#!/usr/bin/env bash
# take.sh <out dir>: play the whole of demo 2 on the toy setup.sh made, as the keeper, off
# screen. asciinema records a headless client of the herdr session buzz to <out>/term.cast;
# review.sh records plannotator's headless page to <out>/browser/. Writes <out>/marks.tsv
# (wall-clock seconds, then a mark) for edit.py, and <out>/take.log.
#
# Before: setup.sh. Detach any other client of buzz: herdr may size panes to the smallest.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:?usage: take.sh <out dir>}
mkdir -p "$out" && out=$(cd "$out" && pwd)
repo=$HOME/demo/buzz size=150x42
die() { echo "take: $*" | tee -a "$out/take.log" >&2; stop_rec; exit 1; }
log() { echo "$(date +%T) $*" | tee -a "$out/take.log"; }
sock=$(herdr session list | awk '$1 == "buzz" && $2 == "running" { print $NF }')
[ -n "$sock" ] || { echo "take: herdr session buzz isn't running: run setup.sh" >&2; exit 1; }
export HERDR_SOCKET_PATH=$sock
unset BS_LOGBOOK BS_PROJECT

now() { perl -MTime::HiRes=time -e 'printf "%.2f\n", time'; }
mark() { printf '%s\t%s\n' "$(now)" "$*" >>"$out/marks.tsv"; log "mark $*"; }
rec= shots=
stop_rec() {
	[ -z "$shots" ] || kill "$shots" 2>/dev/null || true # detach asciinema's herdr client: the cast ends, the session stays
	[ -n "$rec" ] || return 0
	local p c
	for p in $(pgrep -f "herdr session attach buzz"); do
		c=$p
		while [ "$c" -gt 1 ]; do
			[ "$c" = "$rec" ] && { kill "$p"; break; }
			c=$(ps -o ppid= -p "$c" | tr -d ' ')
		done
	done
	wait "$rec" 2>/dev/null || true
	rec=
}
trap stop_rec EXIT

ws_of() { herdr workspace list | jq -r --arg l "$1" '.result.workspaces[] | select(.label == $l) | .workspace_id' | head -1; }
pane_of() { herdr pane list | jq -r --arg w "$1" '[.result.panes[] | select(.workspace_id == $w)][0].pane_id'; }
status_of() { herdr pane list | jq -r --arg p "$1" '.result.panes[] | select(.pane_id == $p) | .agent_status // "unknown"'; }
focus() { herdr workspace focus "$(ws_of "$1")" >/dev/null; }
workers_all() { cat "$out/workers.tsv"; }
workers() { (cd / && bs status --project buzz --json) | jq -r '.[] | select(.running and .session != "queen-buzz") | "\(.label)\t\(.session)"'; }
# until <secs> <what> <cmd...>: poll cmd every 2 s.
until_() {
	local secs=$1 what=$2; shift 2
	local end=$(($(date +%s) + secs))
	until "$@" >/dev/null 2>&1; do
		[ "$(date +%s)" -lt "$end" ] || die "timed out after ${secs}s waiting for $what"
		blocked
		sleep 2
	done
}
# A pane waiting on a permission prompt stops the take: the keeper answers those, not take.sh.
blocked() {
	local p
	p=$(herdr pane list | jq -r '[.result.panes[] | select(.agent_status == "blocked") | .pane_id][0] // empty')
	[ -z "$p" ] || { herdr pane read "$p" --source recent --lines 30 >>"$out/take.log"; die "pane $p is blocked on a prompt (its text is in take.log)"; }
}
# type_into <pane> <text>: type text a few keys at a time, so it reads as typed, then Enter.
type_into() {
	local p=$1 s=$2 i
	for ((i = 0; i < ${#s}; i += 4)); do herdr pane send-text "$p" "${s:i:4}" >/dev/null; sleep 0.03; done
	sleep 0.4 && herdr pane send-keys "$p" enter >/dev/null
}
idle() { [ "$(status_of "$1")" = idle ]; }
working() { [ "$(status_of "$1")" = working ]; }
settle() { # the agent in pane $1 idle for 6 s straight
	until_ 900 "$2 to go idle" idle "$1"
	sleep 6
	idle "$1" || settle "$@"
}

queen=$(pane_of "$(ws_of queen)") keeper=$(pane_of "$(ws_of keeper)")
[ "$queen" != null ] && [ "$keeper" != null ] || { echo "take: no queen or keeper workspace: run setup.sh" >&2; exit 1; }
idle "$queen" || { echo "take: the queen isn't idle" >&2; exit 1; }
: >"$out/marks.tsv"

focus queen
rm -f "$out/term.cast"
env -i HOME="$HOME" USER="$USER" LOGNAME="$USER" SHELL="${SHELL:-/bin/zsh}" PATH="$PATH" \
	TERM=xterm-256color COLORTERM=truecolor LANG="${LANG:-en_US.UTF-8}" \
	asciinema rec -q --headless --window-size "$size" -c "herdr session attach buzz" "$out/term.cast" \
	</dev/null >>"$out/take.log" 2>&1 &
rec=$!
mark start
sleep 3

# Beat 1: the question.
mark beat1
type_into "$queen" "Design study: how should the bee reach the flower? Three workers: straight, zigzag, loop. Each prototypes it in buzz.py and writes STUDY.md (how it looks, trade-offs, open questions), commits both on its branch and reports done with the branch name. No remote, no PR. Don't build yet."
three() { [ "$(workers | wc -l)" -ge 3 ]; }
until_ 300 "three workers" three
workers >"$out/workers.tsv"
mark spawned
sleep 2
# Three branches, three worktrees, from the keeper's shell.
focus keeper
type_into "$keeper" "git worktree list"
sleep 3
mark branches
focus queen
# The workers' panes, recorded by paneshot.py while they study; edit.py stacks them.
specs=()
while IFS=$'\t' read -r l s; do specs+=("$l=$(pane_of "$(ws_of "$l")")"); done < <(workers)
python3 "$here/paneshot.py" "$out" "${specs[@]}" &
shots=$!
mark tiled
studied() { # every worker's branch has STUDY.md, and the worker is idle
	local l s
	while IFS=$'\t' read -r l s; do
		git -C "$repo" cat-file -e "$s:STUDY.md" || return 1
		idle "$(pane_of "$(ws_of "$l")")" || return 1
	done < <(workers)
}
until_ 900 "three studies" studied
mark untiled
kill "$shots"
settle "$queen" queen
mark studied

# Beat 2: the review, in plannotator.
mark beat2
type_into "$queen" "For each branch show me git log --oneline main..<branch>, then run plannotator annotate on the three STUDY.md files with --gate --json in the background. When I decide, have the worker I pick build it and end the other two."
zz=$(workers_all | awk -F'\t' '$1 == "zigzag" { print $2 }')
"$here/review.sh" "$out" "$zz" || die "the review failed"

# Beat 3: the build, with a steer mid-build.
zpane=$(pane_of "$(ws_of zigzag)")
until_ 300 "the zigzag worker to start building" working "$zpane"
mark beat3
sleep 4
focus zigzag
sleep 8
type_into "$zpane" "make the zigzag a bit wider"
mark steered
mark wait
base=$(git -C "$repo" rev-parse "$zz")
built() { [ "$(git -C "$repo" rev-parse "$zz")" != "$base" ] && idle "$zpane"; }
until_ 900 "the zigzag build" built
settle "$zpane" "zigzag worker"
mark built
focus queen
settle "$queen" queen
sleep 3

# Beat 4: merge, run, close out.
mark beat4
focus keeper
sleep 1
type_into "$keeper" "git merge --no-ff $zz -m 'Merge the zigzag bee'"
sleep 2
type_into "$keeper" "clear; git --no-pager log --oneline --graph -6"
sleep 4
type_into "$keeper" "python3 buzz.py"
sleep 8
mark flew
studies=$(workers_all | awk -F'\t' '$1 != "zigzag" { print $2 }' | tr '\n' ' ')
type_into "$keeper" "git branch -D $studies"
sleep 3
mark pruned
focus queen
type_into "$queen" "Close it out: bd close the beads and the epic, bs end the zigzag worker, then bs prune."
sleep 5
mark wait
settle "$queen" queen
mark closed
sleep 3
focus keeper
type_into "$keeper" "clear; python3 buzz.py"
sleep 8
mark end
stop_rec
log "done: $out"
