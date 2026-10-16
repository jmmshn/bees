#!/usr/bin/env bash
# e2e.sh <bead>: check SPEC "Done when" 1-3 with two real Claude Code sessions in herdr.
#
# Starts a queen (e2e-queen) in a plain herdr workspace and spawns a worker for <bead>, a
# throwaway bead of this repo's project, then plays the keeper (e2e-keeper) from this
# shell. Run it from the main checkout; it writes to the project's logbook.
# "Done when" 4 (a killed writer loses nothing) is TestKilledWriterLosesNoCommittedMessage.
set -euo pipefail

bead=${1:?usage: scripts/e2e.sh <throwaway bead, e.g. bees-1.11>}
worker=${bead//./-}
queen=e2e-queen
keeper=e2e-keeper
settings='{"crossSessionInbound":"accept"}' # as bs spawn passes to workers
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
keeper_got() { bs inbox --as "$keeper" --peek | grep -qx "$1"; }

cleanup() {
	bs end --as "$queen" "$worker" >/dev/null 2>&1 || true
	[ -n "${ws:-}" ] && herdr workspace close "$ws" >/dev/null 2>&1 || true
	bs inbox --as "$keeper" >/dev/null 2>&1 || true
}
trap cleanup EXIT

command -v bs >/dev/null || fail "bs is not on PATH (go install ./cmd/bs)"
[ -z "$(session "$queen")$(session "$worker")" ] || fail "$queen or $worker is already running"
bs inbox --as "$keeper" >/dev/null # start from an empty keeper inbox

echo "starting $queen and $worker"
created=$(herdr workspace create --cwd "$repo" --label "$queen" --no-focus)
ws=$(jq -r .result.root_pane.workspace_id <<<"$created")
pane=$(jq -r .result.root_pane.pane_id <<<"$created")
herdr agent start "$queen" --kind claude --pane "$pane" -- --name "$queen" --settings "$settings" \
	"You are $queen, the queen in an automated bs test. Do only what bs messages ask, then stop and wait." >/dev/null
bs spawn --as "$queen" --label "e2e worker" "$bead"
wait_until 180 "$queen to go idle" is_idle "$queen"
wait_until 180 "$worker to go idle" is_idle "$worker"

# 1. An idle worker wakes within 2 s of bs send: the doorbell's time in the worker's
#    transcript against the message id's time (its first 13 hex digits are Unix ms).
id=$(bs send --as "$keeper" "$worker" "e2e 1: reply with exactly: bs send $keeper wake-ok" | awk '/^sent/ {print $2}')
sent_ms=$((16#${id:0:13}))
wait_until 120 "the worker's wake-ok" keeper_got wake-ok
s=$(session "$worker")
# ponytail: Claude names a project's transcript dir after its cwd with every
# non-alphanumeric character as "-" (observed, not documented).
transcript=~/.claude/projects/$(jq -r .cwd <<<"$s" | sed 's/[^A-Za-z0-9]/-/g')/$(jq -r .sessionId <<<"$s").jsonl
rang=$(jq -r 'select(.type == "user") | select(.message.content | tostring | contains("[bs]")) | .timestamp' "$transcript" |
	while read -r ts; do d=$(date -d "$ts" +%s%3N); [ "$d" -ge "$sent_ms" ] && echo "$d" && break; done)
[ -n "$rang" ] || fail "no doorbell in $transcript after the send"
wake=$((rang - sent_ms))
[ "$wake" -le 2000 ] || fail "1. worker woke ${wake} ms after the send, want <= 2000"
pass "1. idle worker woke ${wake} ms after bs send"

# 2. queen -> worker -> queen with nobody typing in either session.
bs send --as "$keeper" "$queen" "e2e 2: send $worker this message: 'reply with exactly: bs send $queen pong'. When $worker replies pong, run: bs send $keeper round-trip-ok" >/dev/null
wait_until 240 "the queen's round-trip-ok" keeper_got round-trip-ok
pass "2. queen -> worker -> queen round trip, nobody typing"

# 3. Mail sent while the worker is not running is delivered once it starts again.
wait_until 60 "$worker to go idle" is_idle "$worker"
kill "$(session "$worker" | jq -r .pid)"
wait_until 30 "$worker to exit" is_gone "$worker"
agent_gone() { ! herdr agent get "$worker" >/dev/null 2>&1; }
wait_until 30 "herdr to clear $worker" agent_gone
bs send --as "$keeper" "$worker" "e2e 3: reply with exactly: bs send $keeper queued-ok" | grep -q "queued; $worker is not running" ||
	fail "3. send to a stopped worker didn't say queued"
bs spawn --as "$queen" "$bead" | grep -q "^resumed $worker" || fail "3. respawn didn't resume $worker"
wait_until 180 "the resumed worker's queued-ok" keeper_got queued-ok
pass "3. mail queued while $worker was stopped was delivered when it resumed"

echo "PASS"
