#!/usr/bin/env bash
# teardown.sh <run dir>: removes what setup.sh and the run made, then checks for leaks.
# Exit 0: all gone. 1: a step failed; later steps were skipped, so fix it and run again.
# 2: torn down, but something leaked outside the toy (listed).
set -uo pipefail
unset BS_LOGBOOK BS_PROJECT # an exported one would point bs at another project's logbook
# pwd -P: herdr reports pane cwds with symlinks resolved, so the run dir must be too.
run=$(cd "${1:?usage: teardown.sh <run dir>}" && pwd -P) || exit 1
# The dir is named after the toy; live.json may be missing if setup died right after mkdir.
toy=$(basename "$run")
[ ! -f "$run/live.json" ] || [ "$(jq -r '.toy // empty' "$run/live.json")" = "$toy" ] || { echo "$run/live.json is not $toy's" >&2; exit 1; }
# Every name below derives from the toy id and the run dir, so refuse anything setup.sh
# couldn't have made: a bad live.json must not end a real project's workers or delete its
# beads, logbook or files.
[[ $toy =~ ^lt[0-9]{16}$ ]] || { echo "$run is not a live-test run dir" >&2; exit 1; }
repo=$run/$toy # never read from live.json
queen_ws=$(jq -r '.workspace // empty' "$run/live.json" 2>/dev/null)
store=$HOME/.local/share/bees
if [ "$(uname)" = Darwin ]; then cache=$HOME/Library/Caches/bees; else cache=${XDG_CACHE_HOME:-$HOME/.cache}/bees; fi
projects=$HOME/.claude/projects enc=$(sed 's/[^A-Za-z0-9]/-/g' <<<"$run") # Claude's name for a project dir
fail() { echo "teardown: $*; fix it and run teardown.sh $run again" >&2; exit 1; }

# The shared bs, unless it predates bs end --forget: then the build under test. Run from /,
# outside any checkout, so bs neither resolves a project from the cwd nor nags about staleness.
bs=$(command -v bs || true)
if [ -z "$bs" ] || ! (cd / && "$bs" end -h 2>&1 | grep -q -- -forget); then
	# The build setup recorded, else (no live.json yet) this checkout's, as setup.sh defaults to.
	bin=$(jq -r '.bin // empty' "$run/live.json" 2>/dev/null) || bin=
	bs=${bin:-$(cd "$(dirname "$0")/../../.." && pwd)/bin}/bs
fi
[ -x "$bs" ] || fail "no bs with end --forget (the shared one is older; build one in the checkout under test)"
endall() {
	[ -f "$store/$toy.db" ] || return 0
	# Only the workers the toy's logbook recorded (motif 12). --forget leaves a workspace herdr
	# has handed to another worktree open; --yes because the queen may still be running.
	(cd / && BS_SESSION=keeper BS_REPOS=$run "$bs" end --all --project "$toy" --force --forget --yes) || fail "bs end --all kept a worker"
}

# 1. Workers, 2. the queen's workspace, 3. workers again: the queen may have spawned one
# between 1 and 2, and the logbook recording it is deleted below.
endall
if [ -n "$queen_ws" ]; then
	panes=$(herdr pane list) || fail "herdr is unreachable, so the queen's workspace $queen_ws may still be open"
	if jq -e --arg ws "$queen_ws" 'any(.result.panes[]; .workspace_id == $ws)' <<<"$panes" >/dev/null; then
		# Only while a pane in it sits in the run dir: herdr can hand the id to a later workspace.
		if jq -e --arg ws "$queen_ws" --arg d "$run/" 'any(.result.panes[]; .workspace_id == $ws and ((.cwd // "") + "/" | startswith($d)))' <<<"$panes" >/dev/null; then
			herdr workspace close "$queen_ws" >/dev/null || fail "herdr workspace close $queen_ws failed"
			echo "closed the queen's workspace $queen_ws"
		else
			echo "workspace $queen_ws has no pane in $run: herdr reused the id; left open"
		fi
	fi
fi
endall

# 4. Beads: every <toy>-* bead, open or closed, deleted rather than closed so none is left.
beads=$(bd query --all --limit 0 --json "id=\"$toy-*\"" | jq -r '.[].id') || fail "bd query failed"
if [ -n "$beads" ]; then
	# shellcheck disable=SC2086 # one id per word
	bd delete --force $beads >/dev/null || fail "bd delete failed"
	echo "deleted beads:" $beads
fi
left=$(bd query --all --limit 0 --json "id=\"$toy-*\"" | jq -r '.[].id' | tr '\n' ' ')
[ -z "$left" ] || fail "beads left: $left"

# 5. Files, last: the logbook goes only once everything it records is gone.
rm -rf -- "$repo" "$repo-WT" || fail "couldn't remove $repo"
# Claude's transcripts of the toy's sessions: kept as the run's record, moved out of ~/.claude.
for t in "$projects/$enc"-*; do
	[ -d "$t" ] || continue
	mkdir -p "$run/keeper/transcripts" && mv -- "$t" "$run/keeper/transcripts/" || fail "couldn't move $t"
done
rm -f -- "$store/$toy.db" "$store/$toy.db-wal" "$store/$toy.db-shm" "$cache/$toy"-*
echo "removed the toy repo, its worktrees, logbook and cache files; kept $run/keeper (with the transcripts)"

# 6. Leaks: anything left by the toy, or written by it into another project.
leaks=0
leak() { echo "LEAK: $*" >&2; leaks=1; }
if ! panes=$(herdr pane list); then
	leak "herdr is unreachable, so a pane may still be open in $run"
elif jq -e --arg d "$run/" 'any(.result.panes[]; (.cwd // "") + "/" | startswith($d))' <<<"$panes" >/dev/null; then
	leak "a herdr pane is still open in $run"
fi
left=$(ls -d "$store/$toy".db* "$cache/$toy"-* "$repo" "$projects/$enc"-* 2>/dev/null | tr '\n' ' ')
[ -z "$left" ] || leak "files left: $left"
# Rows naming the toy in any other logbook. Read from a copy, so the real logbook gets no
# -wal/-shm side files and no write: mode=ro can't open an idle WAL logbook without its -shm.
snap=$(mktemp -d) || fail "mktemp failed"
trap 'rm -rf -- "$snap"' EXIT
rows() {
	rm -f -- "$snap"/l.db*
	cp -- "$1" "$snap/l.db" && { [ ! -f "$1-wal" ] || cp -- "$1-wal" "$snap/l.db-wal"; } || return 1
	python3 -I - "$snap/l.db" "$toy" <<-'EOF'
		import sqlite3, sys
		db, toy = sys.argv[1:]
		like = "%" + toy + "%"
		c = sqlite3.connect(db)
		print(c.execute("SELECT count(*) FROM entries WHERE actor LIKE ? OR subject LIKE ? OR data LIKE ?", (like, like, like)).fetchone()[0])
	EOF
}
for db in "$store"/*.db; do
	[ -e "$db" ] || continue
	n=$(rows "$db") || { leak "couldn't read $db"; continue; }
	[ "$n" = 0 ] || leak "$n logbook rows naming $toy in $db"
done
# Beads in other projects that name the toy: its id is in every session name and path of the run.
stray=$(bd query --all --limit 0 --json "title=$toy OR description=$toy OR notes=$toy" | jq -r '.[] | "\(.id) \(.title)"') || leak "bd query for stray beads failed"
[ -z "$stray" ] || leak "beads naming $toy in other projects: $stray"
[ $leaks = 0 ] || exit 2
echo "no leaks"
