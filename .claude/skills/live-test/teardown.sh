#!/usr/bin/env bash
# teardown.sh <run dir>: stops the toy's herdr session and removes what setup.sh and the
# run made, then checks for leaks.
# Exit 0: all gone. 1: a step failed; later steps were skipped, so fix it and run again.
# 2: torn down, but something the toy owns is left (listed). Leftovers outside the toy are
# printed as notes, never an exit code.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
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
[[ $HOME == /* ]] || { echo "HOME=$HOME is not absolute" >&2; exit 1; }
config=${XDG_CONFIG_HOME:-$HOME/.config}/bees/sessions/$toy # never read from live.json
store=$HOME/.local/share/bees
if [ "$(uname)" = Darwin ]; then cache=$HOME/Library/Caches/bees; else cache=${XDG_CACHE_HOME:-$HOME/.cache}/bees; fi
projects=$HOME/.claude/projects enc=$(sed 's/[^A-Za-z0-9]/-/g' <<<"$run") # Claude's name for a project dir
fail() { echo "teardown: $*; fix it and run teardown.sh $run again" >&2; exit 1; }

# 1. The toy's herdr server, which holds every pane and process the toy started: the
# session is named after the toy, so this stops nothing of the keeper's (motif 12).
# Stopping a session that is already stopped, or deleting one that is gone, is done.
sessions=$(herdr session list) || fail "herdr session list failed"
if awk -v t="$toy" '$1 == t && $2 == "running"' <<<"$sessions" | grep -q .; then
	herdr --session "$toy" server stop >/dev/null || fail "herdr --session $toy server stop failed"
	for _ in $(seq 50); do herdr session list | awk -v t="$toy" '$1 == t && $2 == "running"' | grep -q . || break; sleep 0.2; done
	echo "stopped herdr session $toy"
fi
if herdr session list | awk -v t="$toy" '$1 == t' | grep -q .; then
	herdr session delete "$toy" >/dev/null || fail "herdr session delete $toy failed (still running?)"
	echo "deleted herdr session $toy"
fi

# 2. Beads: every <toy>-* bead, open or closed, deleted rather than closed so none is left.
mine() { bd query --all --limit 0 --json "id=\"$toy-*\"" | jq -r '.[].id'; }
beads=$(mine) || fail "bd query failed"
if [ -n "$beads" ]; then
	# bd delete rewrites a linked bead's references to [deleted:ID], so a link to a bead
	# outside the toy would change a real bead quietly: stop and show it instead.
	# shellcheck disable=SC2086 # one id per word
	links=$(bd show $beads --json | jq -r --arg p "$toy-" '.[] | . as $b | (.dependencies // []) + (.dependents // []) | .[] | select(.id | startswith($p) | not) | "\($b.id) ↔ \(.id)"') || fail "bd show failed"
	[ -z "$links" ] || fail "toy beads are linked to beads outside the toy; remove these links first (bd dep remove): $links"
	# shellcheck disable=SC2086
	bd delete --force $beads >/dev/null || fail "bd delete failed"
	echo "deleted beads:" $beads
fi
left=$(mine | tr '\n' ' ') || fail "bd query failed"
[ -z "$left" ] || fail "beads left: $left"

# 3. Files, last: the logbook goes only once everything it records is gone.
# Delete only absolute paths whose name starts with the toy id (or queen-<toy>-, a bsx
# queen's per-start files), never a bare $HOME-relative one:
# a review agent's rm of $HOME/.local/share/bees/bees.db deleted the real logbook (bees-8.12).
del() {
	for p in "$@"; do
		case $p in /*/"$toy"* | /*/queen-"$toy"-*) ;; *) fail "refusing to delete $p: not an absolute path named after $toy" ;; esac
	done
	rm -rf -- "$@"
}
del "$repo" "$repo-WT" || fail "couldn't remove $repo"
# Claude's transcripts of the toy's sessions: kept as the run's record, moved out of ~/.claude.
for t in "$projects/$enc"-*; do
	[ -d "$t" ] || continue
	dest=$run/keeper/transcripts/$(basename "$t")
	[ ! -e "$dest" ] || dest=$dest.$(date +%s) # a rerun after the session wrote more
	mkdir -p "$run/keeper/transcripts" && mv -- "$t" "$dest" || fail "couldn't move $t"
done
# bsx's per-start files (prompt, settings) are <session>-<key>-* in $cache/bsx (bees-13.4).
del "$store/$toy.db" "$store/$toy.db-wal" "$store/$toy.db-shm" "$run/logbooks" "$cache/$toy"-* "$cache/bsx/$toy"-* "$cache/bsx/queen-$toy"-* "$config" || fail "couldn't remove $toy's logbooks, cache files or config dir"
echo "removed the toy repo, its worktrees, logbooks, cache files and config dir; kept $run/keeper (with the transcripts)"

# 4. Leaks: what the toy owns and teardown should have removed. Exit 2.
leaks=0
leak() { echo "LEAK: $*" >&2; leaks=1; }
if ! sessions=$(herdr session list); then
	leak "herdr is unreachable, so the toy's session may still run"
elif awk -v t="$toy" '$1 == t' <<<"$sessions" | grep -q .; then
	leak "herdr session $toy is still listed"
fi
# The keeper's server: a pane in the run dir means something escaped the toy's session.
if ! panes=$(herdr pane list); then
	leak "the keeper's herdr is unreachable, so a pane may still be open in $run"
elif jq -e --arg d "$run/" 'any(.result.panes[]; (.cwd // "") + "/" | startswith($d))' <<<"$panes" >/dev/null; then
	leak "a pane of the keeper's herdr is open in $run"
fi
# A process the server's stop missed (a detached child) still sits in the run dir.
# Teardown's own ancestors (a keeper shell cd'd into $run/keeper) are not the toy's.
if [ -d /proc ]; then
	stray=$(
		mine=" " p=$BASHPID # this subshell, teardown and up
		while [ "${p:-0}" -gt 1 ]; do mine+="$p " p=$(awk '{print $4}' "/proc/$p/stat" 2>/dev/null); done
		# No pipeline: its subshells would sit in the run dir too.
		for c in /proc/[0-9]*/cwd; do
			d=$(readlink "$c" 2>/dev/null) || continue
			pid=${c//[^0-9]/}
			[[ $d/ == "$run"/* && $mine != *" $pid "* ]] && printf '%s ' "$pid"
		done
	)
	[ -z "$stray" ] || leak "processes still running in $run: $stray"
fi
left=$(ls -d "$store/$toy".db* "$cache/$toy"-* "$cache/bsx/$toy"-* "$cache/bsx/queen-$toy"-* "$repo" "$config" "$projects/$enc"-* 2>/dev/null | tr '\n' ' ')
[ -z "$left" ] || leak "files left: $left"

# 5. Leftovers outside the toy: noted, never an exit code, and left for a later cleanup.
# Beads its sessions filed elsewhere (bd's created_by is git user.name, which setup.sh sets
# to the toy id; bd query can't filter on it, so pick in jq), and rows they wrote into
# other logbooks (read through logbook.py, so no logbook is touched).
note() { echo "note: $*"; }
t0=$(jq -r '.created // empty' "$run/live.json" 2>/dev/null)
if [ -n "$t0" ]; then
	if out=$(bd query --all --limit 0 --json "created>=\"$t0\"" | jq -r --arg t "$toy" '[.[] | select(.created_by == $t) | "\(.id) (\(.title))"] | join(", ")'); then
		[ -z "$out" ] || note "beads $toy filed outside $toy-*: $out"
	else
		note "couldn't list beads $toy filed elsewhere"
	fi
fi
for db in "$store"/*.db; do
	[ -e "$db" ] || continue
	n=$(python3 -I "$here/logbook.py" "$db" "SELECT count(*) FROM entries WHERE actor = ? OR actor LIKE ?" "queen-$toy" "$toy-%") || { note "couldn't read $db"; continue; }
	[ "$n" = 0 ] || note "$n rows written by $toy's sessions in $db"
done
# The keeper's own workspaces, as setup.sh listed them: teardown never touches one, so a
# missing one was closed by someone else. Noted, never a leak.
before=$(jq -c '.keeper_workspaces // empty' "$run/live.json" 2>/dev/null)
if [ -n "$before" ] && [ "$before" != null ] && after=$(herdr workspace list | jq -c '[.result.workspaces[].workspace_id]'); then
	gone=$(jq -nr --argjson b "$before" --argjson a "$after" '$b - $a | join(" ")')
	if [ -n "$gone" ]; then note "the keeper's workspaces $gone closed since setup (not by teardown)"; else echo "the keeper's $(jq length <<<"$before") workspaces from setup are all still open"; fi
fi
[ $leaks = 0 ] || exit 2
echo "no leaks"
