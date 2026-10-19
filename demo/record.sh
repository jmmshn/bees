#!/usr/bin/env bash
# record.sh: record the bs demo GIF (demo/README.md). One take, in steps:
#
#   demo/record.sh start <out>         start the recording and a seeded toy project with its queen
#   demo/record.sh say <out> "<line>"  say a keeper line to the queen; returns when it is idle again
#   demo/record.sh look <out> [secs]   show each worker's pane for secs (default 6), then the queen's
#   demo/record.sh merge <out>         as the keeper, merge the toy's worker branches into its main
#   demo/record.sh stop <out>          end the recording, tear the toy down, render <out>/demo.gif
#
# The take is a herdr session of its own (bees-demo), so the GIF shows only the demo and
# the keeper's own herdr is untouched. Setup and teardown are the live-test skill's.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
live=${LIVE_TEST:-$here/../.claude/skills/live-test} # the live-test skill (PR #52)
session=bees-demo
size=140x40 # 1193x804 at agg --font-size 14: readable at README width

die() { echo "record.sh: $*" >&2; exit 1; }
cmd=${1:-} out=${2:-}
[ -n "$cmd" ] && [ -n "$out" ] || die "usage: record.sh start|say|look|merge|stop <out> ..."
for t in asciinema agg herdr bs jq git; do command -v "$t" >/dev/null || die "needs $t on PATH"; done

h() { herdr --session "$session" "$@"; }
run() { jq -r .run "$out/live.json"; }
pane() { jq -r .pane "$(run)/live.json"; }
queen_ws() { jq -r .workspace "$(run)/live.json"; }
# Every herdr call of live-test's scripts must reach the take's session, not the keeper's.
sock() { herdr session list | awk -v s="$session" '$1 == s { print $NF }'; }

case $cmd in
start)
	[ -x "$live/setup.sh" ] || die "no live-test skill at $live (PR #52); set LIVE_TEST to its directory"
	h workspace list >/dev/null 2>&1 && die "herdr session $session is already running: stop it first"
	mkdir -p "$out" && out=$(cd "$out" && pwd)
	rm -f "$out/demo.cast"
	# A clean environment: the take's panes must not inherit this shell's BS_*, CLAUDE_* or HERDR_* variables.
	(cd "$HOME" && env -i HOME="$HOME" USER="$USER" LOGNAME="$USER" SHELL="${SHELL:-/bin/zsh}" PATH="$PATH" \
		TERM=xterm-256color COLORTERM=truecolor LANG="${LANG:-en_US.UTF-8}" BEADS_DIR="${BEADS_DIR:-}" \
		asciinema rec -q --headless --window-size "$size" -c "herdr session attach $session" "$out/demo.cast" \
		>"$out/asciinema.log" 2>&1 &)
	for _ in $(seq 40); do h workspace list >/dev/null 2>&1 && break; sleep 0.5; done
	first=$(h workspace list | jq -r '.result.workspaces[0].workspace_id') || die "herdr session $session didn't start"
	# The queen runs the shared bs, as a keeper's would.
	HERDR_SOCKET_PATH=$(sock) "$live/setup.sh" "$(dirname "$(command -v bs)")" >"$out/live.json"
	# setup.sh answers the folder-trust prompt, but its keys can land before Claude reads them.
	for _ in $(seq 15); do
		h pane read "$(pane)" | grep -q "Yes, I trust this folder" || break
		h pane send-keys "$(pane)" down enter >/dev/null && sleep 2
	done
	repo=$(jq -r .repo "$out/live.json")
	cp -R "$here/seed/." "$repo/"
	git -C "$repo" add -A && git -C "$repo" commit -qm "wc.py: count the words in a file"
	h workspace close "$first" >/dev/null
	h workspace focus "$(queen_ws)" >/dev/null
	echo "recording; queen pane $(pane); run dir $(run)"
	;;
say)
	line=${3:?usage: record.sh say <out> "<line>"}
	h agent prompt "$(pane)" "$line" >/dev/null
	h agent wait "$(pane)" --until working --timeout 20000 >/dev/null || true # a short turn may already be over
	h agent wait "$(pane)" --timeout 600000 >/dev/null
	;;
look)
	for ws in $(h workspace list | jq -r --arg q "$(queen_ws)" '.result.workspaces[] | select(.workspace_id != $q) | .workspace_id'); do
		h workspace focus "$ws" >/dev/null && sleep "${3:-6}"
	done
	h workspace focus "$(queen_ws)" >/dev/null
	;;
merge)
	repo=$(jq -r .repo "$out/live.json")
	for b in $(git -C "$repo" for-each-ref --format='%(refname:short)' refs/heads | grep -v '^main$'); do
		git -C "$repo" merge -q --no-edit "$b" && echo "merged $b"
	done
	;;
stop)
	# Detach the client: the cast ends, and the session stays up for teardown.
	pkill -f "herdr session attach $session" || true
	for _ in $(seq 20); do pgrep -f "asciinema rec.*$out/demo.cast" >/dev/null || break; sleep 0.5; done
	HERDR_SOCKET_PATH=$(sock) "$live/teardown.sh" "$(run)"
	herdr session stop "$session" >/dev/null
	agg --idle-time-limit 2 --font-size 14 --last-frame-duration 4 "$out/demo.cast" "$out/demo.gif" >/dev/null
	ls -l "$out/demo.cast" "$out/demo.gif"
	;;
*) die "unknown step $cmd" ;;
esac
