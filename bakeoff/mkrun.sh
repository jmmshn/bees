#!/usr/bin/env bash
# mkrun.sh <arm a|b|c> <script, e.g. 4b>: fresh toy repo runs/t<s><arm>/t<s><arm> with a bare origin, then a lead in a herdr workspace.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
arm=$1 s=$2 id=t$2$1
runs=${BAKEOFF_RUNS:-$HOME/.cache/bees-bakeoff/runs} # outside any repo: an ancestor CLAUDE.md would leak into the leads
dir=$runs/$id repo=$runs/$id/$id
[ -e "$dir" ] && { echo "exists: $dir" >&2; exit 1; }
mkdir -p "$dir"
git init -q -b main "$repo"
git -C "$repo" config commit.gpgsign false # toy repo: the signing key is configured only under ~/repos
git -C "$repo" config user.name bakeoff && git -C "$repo" config user.email bakeoff@example.invalid # identity too lives under ~/repos only
cp -r "$here/fixtures/toy/." "$repo/"
git -C "$repo" add -A && git -C "$repo" commit -qm "toy fixtures"
git init -q --bare "$dir/origin.git"
git -C "$repo" remote add origin "$dir/origin.git"
git -C "$repo" push -q -u origin main
git -C "$repo" remote set-head origin main
case $arm in
	a) cmd="claude --name lead-$id --append-system-prompt-file $here/arms/A-subagents.md" ;;
	b) cmd="claude --name lead-$id --append-system-prompt-file $here/arms/B-peers.md" ;;
	c) cmd="bs queen" ;;
esac
name=$([ "$arm" = c ] && echo "queen-$id" || echo "lead-$id")
ws=$(herdr workspace create --cwd "$repo" --label "bakeoff $id" --no-focus)
pane=$(jq -r .result.root_pane.pane_id <<<"$ws")
jq -n --arg id "$id" --arg arm "$arm" --arg s "S$s" --arg repo "$repo" --arg pane "$pane" --arg name "$name" \
	--arg ws "$(jq -r .result.workspace.workspace_id <<<"$ws")" --arg t0 "$(date -u +%FT%T.%3NZ)" \
	'{id:$id,arm:$arm,script:$s,repo:$repo,pane:$pane,lead:$name,workspace:$ws,created:$t0}' > "$dir/run.json"
herdr pane run "$pane" "$cmd" >/dev/null
cat "$dir/run.json"
# The captain trusts the new folder once (Claude keys trust by repo root), as a human would.
for _ in $(seq 30); do
	herdr pane read "$pane" 2>/dev/null | grep -q "Yes, I trust this folder" && { herdr pane send-keys "$pane" down enter >/dev/null; break; }
	sleep 1
done
