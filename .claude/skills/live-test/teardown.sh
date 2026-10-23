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
# ...but checked against it: a teardown run with another XDG_CONFIG_HOME than setup's would
# delete and leak-check a dir that was never the toy's, and miss the real one.
recorded=$(jq -r '.config // empty' "$run/live.json" 2>/dev/null)
[ -z "$recorded" ] || [ "$recorded" = "$config" ] || { echo "setup wrote $toy's config to $recorded, but this shell's XDG_CONFIG_HOME names $config: run teardown with setup's environment" >&2; exit 1; }
store=$HOME/.local/share/bees
if [ "$(uname)" = Darwin ]; then cache=$HOME/Library/Caches/bees; else cache=${XDG_CACHE_HOME:-$HOME/.cache}/bees; fi
projects=$HOME/.claude/projects enc=$(sed 's/[^A-Za-z0-9]/-/g' <<<"$run") # Claude's name for a project dir
fail() { echo "teardown: $*; fix it and run teardown.sh $run again" >&2; exit 1; }
# agy's settings and data dir, checked against setup's like the config dir. agy keeps the
# toy's trust entries and conversations there, outside the run dir (bees-13.13.8).
agy_settings=${AGY_SETTINGS:-$HOME/.gemini/antigravity-cli/settings.json} agy_data=
recorded=$(jq -r '.agy_settings // empty' "$run/live.json" 2>/dev/null)
[ -z "$recorded" ] || [ "$recorded" = "$agy_settings" ] || { echo "setup used agy settings $recorded, but this shell's AGY_SETTINGS names $agy_settings: run teardown with setup's environment" >&2; exit 1; }
[ -d "$(dirname "$agy_settings")/log" ] && agy_data=$(dirname "$agy_settings")
# agy_logs: agy's logs of a start whose workspace (its cwd, its --add-dir) is in the run dir;
# agy_cids: the conversations those starts created.
agy_logs() {
	[ -n "$agy_data" ] || return 0
	for l in "$agy_data"/log/cli-*.log; do
		[ -f "$l" ] && grep -m1 'workspaceDirs=' "$l" | grep -qF -e "[$run/" -e " $run/" && echo "$l"
	done
}
agy_cids() { agy_logs | while read -r l; do sed -n 's/.*Created conversation \([0-9a-f-]\{36\}\) .*/\1/p' "$l"; done | sort -u; }
# agy_files <cid>...: what agy keeps of each conversation.
agy_files() { for c in "$@"; do ls -d "$agy_data/conversations/$c".db* "$agy_data/brain/$c" "$agy_data/annotations/$c.pbtxt" "$agy_data/implicit/$c.pb" 2>/dev/null; done; }
# untrust [--check]: removes (or with --check, lists) agy's trustedWorkspaces entries in the run
# dir, under the lock bs's own edits take, rewriting the file only if it is unchanged since read.
untrust() {
	[ -f "$agy_settings" ] || return 0
	python3 -I - "$agy_settings" "$run" "$@" <<'PY'
import fcntl, json, os, sys, tempfile, time
path, run, check = sys.argv[1], sys.argv[2], "--check" in sys.argv
mine = lambda p: p == run or p.startswith(run + "/")
with open(path + ".lock", "a") as lock:
    for _ in range(200):
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            break
        except BlockingIOError:
            time.sleep(0.05)
    else:
        sys.exit(f"{path}.lock: another bs held it for 10 s")
    for _ in range(3):
        old = open(path, "rb").read()
        keys = json.loads(old or b"{}")
        ds = keys.get("trustedWorkspaces") or []
        gone = [d for d in ds if mine(d)]
        if check or not gone:
            if gone:
                print(" ".join(gone))
            sys.exit(0)
        keys["trustedWorkspaces"] = [d for d in ds if not mine(d)]
        fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path), prefix=os.path.basename(path) + ".lt-")
        with os.fdopen(fd, "w") as f:
            f.write(json.dumps(keys, indent=2) + "\n")
        os.chmod(tmp, os.stat(path).st_mode & 0o777)
        if open(path, "rb").read() != old:  # agy rewrote it meanwhile: read it again
            os.remove(tmp)
            continue
        os.rename(tmp, path)
        print("untrusted in agy:", " ".join(gone))
        sys.exit(0)
    sys.exit(f"{path} kept changing")
PY
}

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
# Delete only absolute paths whose name starts with the toy id (or queen-<toy>-, a
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
# agy's trust entries for the run dir (spawn's, or a keeper's answer to agy's prompt), and its
# logs and conversations of the toy's starts: moved into the run's record like the transcripts.
untrust || fail "couldn't remove the toy's trust entries from $agy_settings"
if [ -n "$agy_data" ]; then
	# shellcheck disable=SC2046 # one cid per word
	set -- $(agy_logs) $(agy_files $(agy_cids))
	if [ $# -gt 0 ]; then
		mkdir -p "$run/keeper/agy" && mv -- "$@" "$run/keeper/agy/" || fail "couldn't move agy's files of $toy"
		echo "moved $# agy logs and conversation files into $run/keeper/agy"
	fi
fi
# bs's per-start files (prompt, settings) are <session>-<key>-* in $cache/bs (bees-13.4);
# $cache/bsx held them before the swap (bees-13.9).
del "$store/$toy.db" "$store/$toy.db-wal" "$store/$toy.db-shm" "$run/logbooks" "$cache/$toy"-* "$cache/bs/$toy"-* "$cache/bs/queen-$toy"-* "$cache/bsx/$toy"-* "$cache/bsx/queen-$toy"-* "$config" || fail "couldn't remove $toy's logbooks, cache files or config dir"
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
else
	unchecked="stray processes (no /proc to look in)" # couldn't look is not "none" (Rule G)
	echo "can't check for processes left in $run: no /proc"
fi
left=$(ls -d "$store/$toy".db* "$cache/$toy"-* "$cache/bs/$toy"-* "$cache/bs/queen-$toy"-* "$cache/bsx/$toy"-* "$cache/bsx/queen-$toy"-* "$repo" "$config" "$projects/$enc"-* 2>/dev/null | tr '\n' ' ')
[ -z "$left" ] || leak "files left: $left"
left=$(untrust --check) && [ -z "$left" ] || leak "agy trust entries left in $agy_settings: ${left:-unreadable}"
# shellcheck disable=SC2046
[ -z "$agy_data" ] || { left=$(agy_logs; agy_files $(agy_cids)) && [ -z "$left" ]; } || leak "agy files left: $left"

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
# agy's shared files the toy's starts wrote lines into, which agy rewrites itself.
if [ -n "$agy_data" ]; then
	n=$(grep -cF "\"workspace\":\"$run/" "$agy_data/history.jsonl" 2>/dev/null) && [ "$n" != 0 ] && note "$n lines of $toy's prompts in $agy_data/history.jsonl"
	n=$(jq -r --arg d "$run/" '[keys[] | select(startswith($d))] | length' "$agy_data/cache/last_conversations.json" 2>/dev/null) && [ "$n" != 0 ] && note "$n of $toy's folders in $agy_data/cache/last_conversations.json"
fi
# The keeper's own workspaces, as setup.sh listed them: teardown never touches one, so a
# missing one was closed by someone else. Reported, never a leak: this shows the keeper's
# server is still answering for them, not a check teardown can fail.
before=$(jq -c '.keeper_workspaces // empty' "$run/live.json" 2>/dev/null)
if [ -n "$before" ] && [ "$before" != null ] && after=$(herdr workspace list | jq -c '[.result.workspaces[].workspace_id]'); then
	gone=$(jq -nr --argjson b "$before" --argjson a "$after" '$b - $a | join(" ")')
	if [ -n "$gone" ]; then note "the keeper's workspaces $gone closed since setup (not by teardown)"; else echo "the keeper's $(jq length <<<"$before") workspaces from setup are all still open"; fi
fi
[ $leaks = 0 ] || exit 2
echo "no leaks${unchecked:+ found; not checked: $unchecked}"
