#!/usr/bin/env bash
# e2e.sh <bead>: check SPEC "Done when" 1-3 with two real Claude Code sessions in herdr.
#
# Starts a chief (e2e-chief) in a plain herdr workspace and spawns a peon for <bead>, a
# throwaway bead of this repo's project, then plays the captain (e2e-captain) from this
# shell. Run it from the main checkout; it writes to the project's logbook.
# "Done when" 4 (a killed writer loses nothing) is TestKilledWriterLosesNoCommittedMessage.
set -euo pipefail

bead=${1:?usage: scripts/e2e.sh <throwaway bead, e.g. peons-1.11>}
peon=${bead//./-}
chief=e2e-chief
captain=e2e-captain
settings='{"crossSessionInbound":"accept"}'
repo=$(git rev-parse --show-toplevel)
cd "$repo"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok   $*"; }

# session <name>: the live registry entry for a Claude session name, or nothing.
session() {
	for f in ~/.claude/sessions/*.json; do
		jq -c --arg n "$1" 'select(.name == $n)' "$f" 2>/dev/null || true
	done | tail -1
}

# wait_until <seconds> <what> <command...>: poll until the command succeeds.
wait_until() {
	local secs=$1 what=$2
	shift 2
	for _ in $(seq "$secs"); do
		"$@" >/dev/null 2>&1 && return 0
		sleep 1
	done
	fail "timed out after ${secs}s waiting for $what"
}

is_idle() { [ "$(session "$1" | jq -r .status)" = idle ]; }
is_gone() { [ -z "$(session "$1")" ]; }
captain_got() { pns inbox --as "$captain" --peek | grep -qx "$1"; }

cleanup() {
	pns end --as "$chief" "$peon" >/dev/null 2>&1 || true
	[ -n "${ws:-}" ] && herdr workspace close "$ws" >/dev/null 2>&1 || true
	pns inbox --as "$captain" >/dev/null 2>&1 || true
}
trap cleanup EXIT

command -v pns >/dev/null || fail "pns is not on PATH (go install ./cmd/pns)"
[ -z "$(session "$chief")$(session "$peon")" ] || fail "$chief or $peon is already running"
pns inbox --as "$captain" >/dev/null # start from an empty captain inbox

echo "starting $chief and $peon"
created=$(herdr workspace create --cwd "$repo" --label "$chief" --no-focus)
ws=$(jq -r .result.root_pane.workspace_id <<<"$created")
pane=$(jq -r .result.root_pane.pane_id <<<"$created")
herdr agent start "$chief" --kind claude --pane "$pane" -- --name "$chief" --settings "$settings" \
	"You are $chief, the chief in an automated pns test. Do only what pns messages ask, then stop and wait." >/dev/null
pns spawn --as "$chief" "$bead"
wait_until 180 "$chief to go idle" is_idle "$chief"
wait_until 180 "$peon to go idle" is_idle "$peon"

# 1. An idle peon wakes within 2 s of pns send: the doorbell's time in the peon's
#    transcript against the message id's time (its first 13 hex digits are Unix ms).
id=$(pns send --as "$captain" "$peon" "e2e 1: reply with exactly: pns send $captain wake-ok" | awk '/^sent/ {print $2}')
sent_ms=$((16#${id:0:13}))
wait_until 120 "the peon's wake-ok" captain_got wake-ok
s=$(session "$peon")
transcript=~/.claude/projects/$(jq -r .cwd <<<"$s" | sed 's/[^A-Za-z0-9]/-/g')/$(jq -r .sessionId <<<"$s").jsonl
rang=$(jq -r 'select(.type == "user") | select(.message.content | tostring | contains("[pns]")) | .timestamp' "$transcript" |
	while read -r ts; do d=$(date -d "$ts" +%s%3N); [ "$d" -ge "$sent_ms" ] && echo "$d" && break; done)
[ -n "$rang" ] || fail "no doorbell in $transcript after the send"
wake=$((rang - sent_ms))
[ "$wake" -le 2000 ] || fail "1. peon woke ${wake} ms after the send, want <= 2000"
pass "1. idle peon woke ${wake} ms after pns send"

# 2. chief -> peon -> chief with nobody typing in either session.
pns send --as "$captain" "$chief" "e2e 2: send $peon this message: 'reply with exactly: pns send $chief pong'. When $peon replies pong, run: pns send $captain round-trip-ok" >/dev/null
wait_until 240 "the chief's round-trip-ok" captain_got round-trip-ok
pass "2. chief -> peon -> chief round trip, nobody typing"

# 3. Mail sent while the peon is not running is delivered once it starts again.
wait_until 60 "$peon to go idle" is_idle "$peon"
kill "$(session "$peon" | jq -r .pid)"
wait_until 30 "$peon to exit" is_gone "$peon"
sleep 2 # let herdr notice the agent exited
pns send --as "$captain" "$peon" "e2e 3: reply with exactly: pns send $captain queued-ok" | grep -q "queued; $peon is not running" ||
	fail "3. send to a stopped peon didn't say queued"
pns spawn --as "$chief" "$bead" | grep -q "^resumed $peon" || fail "3. respawn didn't resume $peon"
wait_until 180 "the resumed peon's queued-ok" captain_got queued-ok
pass "3. mail queued while $peon was stopped was delivered when it resumed"

echo "PASS"
