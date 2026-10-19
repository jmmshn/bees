#!/usr/bin/env bash
# take.sh <out dir>: play the whole of demo 2 on the toy setup.sh made, as the keeper, while
# screencapture records the display to <out>/take.mov. Writes <out>/marks.tsv (seconds from
# the start of the recording, then a mark) for edit.sh, and <out>/take.log.
#
# Before: setup.sh, and the herdr session buzz attached in a full-screen terminal
# (herdr session attach buzz), which is what the recording shows. Hands off the Mac.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:?usage: take.sh <out dir>}
mkdir -p "$out" && out=$(cd "$out" && pwd)
repo=$HOME/demo/buzz cdp=9223 # stage.sh opens Chrome's DevTools port here
die() { echo "take: $*" | tee -a "$out/take.log" >&2; stop_rec; exit 1; }
log() { echo "$(date +%T) $*" | tee -a "$out/take.log"; }
sock=$(herdr session list | awk '$1 == "buzz" && $2 == "running" { print $NF }')
[ -n "$sock" ] || { echo "take: herdr session buzz isn't running: run setup.sh" >&2; exit 1; }
export HERDR_SOCKET_PATH=$sock
unset BS_LOGBOOK BS_PROJECT

now() { perl -MTime::HiRes=time -e 'printf "%.2f\n", time'; }
mark() { echo "$(awk -v a="$(now)" -v b="$t0" 'BEGIN { printf "%.2f", a - b }')	$*" >>"$out/marks.tsv"; log "mark $*"; }
rec=
stop_rec() { [ -z "$rec" ] || { kill -INT "$rec" 2>/dev/null; wait "$rec" 2>/dev/null || true; rec=; }; }
trap stop_rec EXIT

ws_of() { herdr workspace list | jq -r --arg l "$1" '.result.workspaces[] | select(.label == $l) | .workspace_id' | head -1; }
pane_of() { herdr pane list | jq -r --arg w "$1" '[.result.panes[] | select(.workspace_id == $w)][0].pane_id'; }
status_of() { herdr pane list | jq -r --arg p "$1" '.result.panes[] | select(.pane_id == $p) | .agent_status // "unknown"'; }
focus() { herdr workspace focus "$(ws_of "$1")" >/dev/null; }
workers() { (cd / && bs status --project buzz --json) | jq -r '.[] | select(.running and .session != "queen-buzz") | "\(.label)\t\(.session)"'; }
# until <secs> <what> <cmd...>: poll cmd every 2 s.
until_() {
	local secs=$1 what=$2; shift 2
	local end=$(($(date +%s) + secs))
	until "$@" >/dev/null 2>&1; do
		[ "$(date +%s)" -lt "$end" ] || die "timed out after ${secs}s waiting for $what"
		sleep 2
	done
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
screencapture -v -x "$out/take.mov" </dev/null >>"$out/take.log" 2>&1 &
rec=$! t0=$(now)
sleep 2
mark start

# Beat 1: the question.
mark beat1
type_into "$queen" "Design study: how should the bee reach the flower? Three workers: straight, zigzag, loop. Each prototypes it in buzz.py and writes STUDY.md (how it looks, trade-offs, open questions), commits both on its branch and reports done with the branch name. No remote, no PR. Don't build yet."
three() { [ "$(workers | wc -l)" -ge 3 ]; }
until_ 300 "three workers" three
mark spawned
sleep 3
for l in straight zigzag loop; do focus "$l"; sleep 4; done
focus queen
mark wait
studied() { # every worker's branch has STUDY.md, and the worker is idle
	local l s
	while IFS=$'\t' read -r l s; do
		git -C "$repo" cat-file -e "$s:STUDY.md" || return 1
		idle "$(pane_of "$(ws_of "$l")")" || return 1
	done < <(workers)
}
until_ 900 "three studies" studied
settle "$queen" queen
mark studied

# Beat 2: the review, in plannotator.
mark beat2
type_into "$queen" "For each branch show me git log --oneline main..<branch>, then run plannotator annotate on the three STUDY.md files with --gate --json in the background. When I decide, have the worker I pick build it and end the other two."
page() { curl -fsS "http://127.0.0.1:$cdp/json/list" | jq -e 'any(.[]; .type == "page" and (.url | startswith("http")))'; }
until_ 300 "plannotator's page" page
js() { node "$here/cdp.mjs" "$cdp" "$@"; }
B='(t)=>[...document.querySelectorAll("button,[role=menuitem]")].find(b=>b.innerText.trim().startsWith(t)||b.title===t)'
ready() { [ "$(js "!!($B)('Approve')")" = true ]; }
until_ 60 "the Approve button" ready
mark browser
sleep 2
# Dialogs a first visit shows; setup.sh's warm-up should have seen them already.
js "($B)('Continue')?.click(), ($B)('Got it')?.click()" >/dev/null
# Show each study, then settle on zigzag's.
zz=$(workers | awk -F'\t' '$1 == "zigzag" { print $2 }')
n=$(js '[...document.querySelectorAll("button")].filter(b=>b.title.endsWith("STUDY.md")).length')
for ((i = 0; i < n; i++)); do
	js "[...document.querySelectorAll('button')].filter(b=>b.title.endsWith('STUDY.md'))[$i].click()" >/dev/null
	sleep 3
done
js "[...document.querySelectorAll('button')].filter(b=>b.title.endsWith('STUDY.md')).find(b=>b.title.includes('$zz'))?.click()" >/dev/null
sleep 2
js "($B)('Add global comment').click()" >/dev/null
sleep 1
js 'document.querySelector("textarea").focus()' >/dev/null
js --type "zigzag, end with a buzz" >/dev/null
sleep 1
js "($B)('Add').click()" >/dev/null
sleep 1.5
js "($B)('More decisions').click()" >/dev/null
sleep 1.5
js "($B)('Approve with notes').click()" >/dev/null
mark approved
gone() { ! curl -fsS "http://127.0.0.1:$cdp/json/list"; }
until_ 30 "the browser to close" gone
mark terminal

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
type_into "$keeper" "git log --oneline --graph"
sleep 3
type_into "$keeper" "python3 buzz.py"
sleep 8
mark flew
focus queen
type_into "$queen" "Close it out: bd close the beads and the epic, bs end the zigzag worker, bs prune, and git branch -D the two unmerged study branches."
sleep 5
mark wait
settle "$queen" queen
mark closed
sleep 4
focus keeper
type_into "$keeper" "clear; python3 buzz.py"
sleep 8
mark end
stop_rec
log "done: $out/take.mov"
