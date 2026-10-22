#!/usr/bin/env bash
# teardown.sh: removes what setup.sh and the demo-2 run made, then checks for leaks.
# Exit 0: all gone. 1: a step failed; fix it and run again (it is idempotent). 2: leaks (listed).
#
# It removes only the absolute paths below, the buzz-* beads and the herdr session buzz;
# Claude's transcripts of the toy's sessions move to $KEEP (default ~/.cache/bees-demo2/<time>).
set -uo pipefail
# demo2 drives the old bs (bs status --project … --json, bs end --project); the new bs
# reads a herdr --session's own config, which setup.sh doesn't write yet (bees-13.33).
echo "teardown.sh: demo2 isn't ported to the new bs yet (bees-13.33); nothing was done" >&2
exit 1
unset BS_LOGBOOK BS_PROJECT
session=buzz
fail() { echo "teardown: $*; fix it and run teardown.sh again" >&2; exit 1; }
[[ $HOME == /* ]] || fail "HOME=$HOME is not absolute"
root=$HOME/demo repo=$HOME/demo/buzz stage=$HOME/demo/.stage
store=$HOME/.local/share/bees cache=$HOME/Library/Caches/bees
[ "$(uname)" = Darwin ] || cache=${XDG_CACHE_HOME:-$HOME/.cache}/bees
projects=$HOME/.claude/projects enc=$(sed 's/[^A-Za-z0-9]/-/g' <<<"$repo") # Claude's name for a project dir
keep=${KEEP:-$HOME/.cache/bees-demo2/$(date +%Y%m%d-%H%M%S)}
sock() { herdr session list | awk -v s="$session" '$1 == s && $2 == "running" { print $NF }'; }

# 1. Workers the toy's logbook records as running, through bs, so their worktrees go too.
sock=$(sock)
if [ -n "$sock" ] && [ -f "$store/buzz.db" ]; then
	for w in $(cd / && bs status --project buzz --json | jq -r '.[] | select(.running and .session != "queen-buzz") | .session'); do
		(cd / && HERDR_SOCKET_PATH=$sock BS_SESSION=keeper bs end --project buzz --force "$w") || fail "bs end $w failed"
	done
fi
# 2. The herdr session: its panes, the queen with them.
if herdr session list | awk -v s="$session" '$1 == s' | grep -q .; then
	[ -z "$sock" ] || herdr session stop "$session" >/dev/null || fail "herdr session stop $session failed"
	herdr session delete "$session" >/dev/null || fail "herdr session delete $session failed"
	echo "stopped and deleted herdr session $session"
fi
# 3. Beads: every buzz-* bead, deleted rather than closed.
beads=$(bd query --all --limit 0 --json 'id="buzz-*"' | jq -r '.[].id') || fail "bd query failed"
if [ -n "$beads" ]; then
	# shellcheck disable=SC2086 # one id per word
	bd delete --force $beads >/dev/null || fail "bd delete failed"
	echo "deleted beads:" $beads
fi
# 4. Files: worktrees and branches go with the repo. Transcripts are kept as the run's record.
[ ! -d "$repo" ] || git -C "$repo" worktree prune
for t in "$projects/$enc"*; do
	[ -d "$t" ] || continue
	mkdir -p "$keep" && mv -- "$t" "$keep/" || fail "couldn't move $t"
done
rm -rf -- "$repo" "$repo-WT" "$stage" "$store/buzz.db" "$store/buzz.db-wal" "$store/buzz.db-shm" "$cache"/buzz-* ||
	fail "couldn't remove the toy's files"
rmdir -- "$root" 2>/dev/null || true # only if empty: the keeper may keep a recording there
[ ! -d "$keep" ] || echo "kept the toy's transcripts in $keep"

# 5. Leaks.
leaks=0
leak() { echo "LEAK: $*" >&2; leaks=1; }
! herdr session list | awk -v s="$session" '$1 == s' | grep -q . || leak "herdr session $session is still listed"
left=$(ls -d "$repo" "$repo-WT" "$stage" "$store"/buzz.db* "$cache"/buzz-* "$projects/$enc"* 2>/dev/null | tr '\n' ' ')
[ -z "$left" ] || leak "files left: $left"
left=$(bd query --all --limit 0 --json 'id="buzz-*"' | jq -r '.[].id' | tr '\n' ' ')
[ -z "$left" ] || leak "beads left: $left"
# Rows the toy's sessions wrote into another project's logbook, read from a copy so no logbook is touched.
snap=$(mktemp -d) && trap 'rm -rf -- "$snap"' EXIT
for db in "$store"/*.db; do
	[ -e "$db" ] || continue
	rm -f -- "$snap"/l.db*
	cp -- "$db" "$snap/l.db" && { [ ! -f "$db-wal" ] || cp -- "$db-wal" "$snap/l.db-wal"; } || { leak "couldn't read $db"; continue; }
	n=$(python3 -I -c 'import sqlite3,sys; print(sqlite3.connect(sys.argv[1]).execute("SELECT count(*) FROM entries WHERE actor = ? OR actor LIKE ?", ("queen-buzz", "buzz-%")).fetchone()[0])' "$snap/l.db") ||
		{ leak "couldn't read $db"; continue; }
	[ "$n" = 0 ] || leak "$n rows by the toy's sessions in $db"
done
[ $leaks = 0 ] || exit 2
echo "no leaks"
