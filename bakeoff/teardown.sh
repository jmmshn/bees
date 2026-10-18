#!/usr/bin/env bash
# teardown.sh <run dir>: close the run's herdr workspaces, bd close its open beads, rm its logbook. Keeps the run dir.
# Tears down only what the run recorded (motif 12): the lead's workspace from run.json, workers'
# from the run's logbook spawn entries and collect.sh's leftovers; never a guess from herdr's list.
set -uo pipefail
dir=$(cd "$1" && pwd) && run=$(cat "$dir/run.json") || exit 1
id=$(jq -r .id <<<"$run")
# A run id names the beads (<id>-N) and the logbook (<id>.db): refuse anything but a bake-off id,
# so a bad run.json can't close a real project's beads or delete its logbook.
[[ $id =~ ^t[0-9]+[a-z]*$ ]] || { echo "not a bake-off run id: $id" >&2; exit 1; }
db=$HOME/.local/share/bees/$id.db
spawned() {
	[ -f "$db" ] || return 0
	python3 -I - "$db" <<-'EOF'
		import json, sqlite3, sys
		c = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
		for (d,) in c.execute("select data from entries where kind = 'spawned'"):
		    print(json.loads(d)["workspace"])
	EOF
}
leftover() { [ -f "$dir/metrics.json" ] && jq -r '.leftovers.workspaces[]?' "$dir/metrics.json"; }
lead=$(jq -r .workspace <<<"$run")
# Workers before the lead: herdr won't close a workspace whose linked worktree workspaces are open.
mapfile -t wss < <({ spawned; leftover; } | grep -vx -e null -e "$lead" | sort -u; echo "$lead")
# herdr can hand a recorded id to a later workspace: close one only while a pane in it sits in the run dir.
ours() { herdr pane list | jq -e --arg ws "$1" --arg d "$dir/" 'any(.result.panes[]; .workspace_id == $ws and ((.cwd // "") + "/" | startswith($d)))' >/dev/null; }
for ws in "${wss[@]}"; do
	if ! herdr workspace get "$ws" >/dev/null 2>&1; then echo "workspace $ws: already gone"
	elif ! ours "$ws"; then echo "workspace $ws: no pane in $dir, left open" >&2
	elif out=$(herdr workspace close "$ws" 2>&1); then echo "closed workspace $ws"
	else echo "workspace $ws: $out" >&2; fi
done
open() { bd query --limit 0 --json "id=\"$id-*\" AND status!=closed" | jq -r '.[].id'; }
# Children first: bd won't close an epic with an open child (and still exits 0).
mapfile -t beads < <(open | awk -F. '{print NF, $0}' | sort -rn | cut -d' ' -f2)
# Never call bd close with no ids: it closes the last touched bead.
[ ${#beads[@]} -gt 0 ] && bd close "${beads[@]}" --reason "bake-off $id torn down" >/dev/null
left=$(open)
closed=$(printf '%s\n' "${beads[@]}" | grep -vxF -e "$left" -e '' | paste -sd' ')
[ -n "$closed" ] && echo "closed beads $closed"
[ -n "$left" ] && echo "beads still open: $(paste -sd' ' <<<"$left")" >&2
for f in "$db" "$db-wal" "$db-shm"; do [ -e "$f" ] && rm -- "$f" && echo "removed $f"; done
exit 0
