#!/usr/bin/env bash
# teardown.sh <run dir>: removes what setup.sh and the run made, then checks for leaks.
# Exit 0: all gone. 1: a step failed; later steps were skipped, so fix it and run again.
# 2: torn down, but something leaked outside the toy (listed).
set -uo pipefail
run=$(cd "${1:?usage: teardown.sh <run dir>}" && pwd) || exit 1
toy=$(jq -r .toy "$run/live.json") || exit 1
# Every name below derives from the toy id, so refuse anything setup.sh couldn't have made:
# a bad live.json must not end a real project's workers or delete its beads or logbook.
[[ $toy =~ ^lt[0-9]{12}$ ]] || { echo "not a live-test toy: $toy" >&2; exit 1; }
bin=$(jq -r .bin "$run/live.json") repo=$(jq -r .repo "$run/live.json")
queen_ws=$(jq -r .workspace "$run/live.json") t0=$(jq -r .created "$run/live.json")
store=$HOME/.local/share/bees
if [ "$(uname)" = Darwin ]; then cache=$HOME/Library/Caches/bees; else cache=${XDG_CACHE_HOME:-$HOME/.cache}/bees; fi
fail() { echo "teardown: $*; fix it and run teardown.sh $run again" >&2; exit 1; }

# 1. Workers: only those the toy's logbook recorded (motif 12). Exits nonzero if any is kept.
if [ -f "$store/$toy.db" ]; then
	BS_SESSION=keeper BS_REPOS=$(dirname "$repo") "$bin/bs" end --all --project "$toy" --force || fail "bs end --all kept a worker"
fi

# 2. The queen's workspace: the id setup recorded, and only while a pane in it sits in the run
# dir, since herdr can hand the id to a later workspace.
ours() { herdr pane list | jq -e --arg ws "$1" --arg d "$run/" 'any(.result.panes[]; .workspace_id == $ws and ((.cwd // "") + "/" | startswith($d)))' >/dev/null; }
panes=$(herdr pane list) || fail "herdr is unreachable, so the queen's workspace $queen_ws may still be open"
if jq -e --arg ws "$queen_ws" 'any(.result.panes[]; .workspace_id == $ws)' <<<"$panes" >/dev/null; then
	ours "$queen_ws" || fail "workspace $queen_ws has no pane in $run: herdr reused the id; left open"
	herdr workspace close "$queen_ws" >/dev/null || fail "herdr workspace close $queen_ws failed"
	echo "closed the queen's workspace $queen_ws"
fi

# 3. Beads: every <toy>-* bead, open or closed, deleted rather than closed so none is left.
beads=$(bd query --all --limit 0 --json "id=\"$toy-*\"" | jq -r '.[].id') || fail "bd query failed"
if [ -n "$beads" ]; then
	# shellcheck disable=SC2086 # one id per word
	bd delete --force $beads >/dev/null || fail "bd delete failed"
	echo "deleted beads:" $beads
fi
left=$(bd query --all --limit 0 --json "id=\"$toy-*\"" | jq -r '.[].id' | tr '\n' ' ')
[ -z "$left" ] || fail "beads left: $left"

# 4. Files, last: the logbook goes only once everything it records is gone.
rm -rf -- "$repo" "$repo-WT" || fail "couldn't remove $repo"
# Claude's transcripts of the toy's sessions: kept as the run's record, moved out of ~/.claude.
projects=$HOME/.claude/projects enc=$(tr '/.' '--' <<<"$run")
for t in "$projects/$enc"-*; do
	[ -d "$t" ] || continue
	mkdir -p "$run/keeper/transcripts" && mv -- "$t" "$run/keeper/transcripts/" || fail "couldn't move $t"
done
rm -f -- "$store/$toy.db" "$store/$toy.db-wal" "$store/$toy.db-shm" "$cache/$toy"-*
echo "removed the toy repo, its worktrees, logbook and cache files; kept $run/keeper (with the transcripts)"

# 5. Leaks: anything left by the toy, or written by it into another project.
leaks=0
leak() { echo "LEAK: $*" >&2; leaks=1; }
herdr pane list | jq -e --arg d "$run/" 'any(.result.panes[]; (.cwd // "") + "/" | startswith($d))' >/dev/null && leak "a herdr pane is still open in $run"
left=$(ls -d "$store/$toy".db* "$cache/$toy"-* "$repo" "$projects/$enc"-* 2>/dev/null | tr '\n' ' ')
[ -z "$left" ] || leak "files left: $left"
# python3, not the sqlite3 CLI: Claude Code's sandbox stops sqlite3 opening the logbooks.
rows() {
	python3 -I - "$1" "$toy" <<-'EOF'
		import sqlite3, sys
		db, toy = sys.argv[1], sys.argv[2]
		c = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
		print(c.execute("SELECT count(*) FROM entries WHERE actor LIKE ? OR subject LIKE ? OR ? IN (actor, subject)", (toy + "-%", toy + "-%", "queen-" + toy)).fetchone()[0])
	EOF
}
for db in "$store"/*.db; do
	[ -e "$db" ] || continue
	n=$(rows "$db") || { leak "couldn't read $db"; continue; }
	[ "$n" = 0 ] || leak "$n logbook rows naming $toy in $db"
done
other=$(bd query --all --limit 0 --json "created>=\"$t0\"" | jq -r '.[] | "\(.id)  \(.title)"')
if [ -n "$other" ]; then
	echo "beads created in other projects since setup ($t0); any the run filed is a leak:" >&2
	echo "$other" >&2
fi
[ $leaks = 0 ] || exit 2
echo "no leaks"
