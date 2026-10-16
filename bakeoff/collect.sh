#!/usr/bin/env bash
# collect.sh <run dir>: metrics for one run (timing, keeper turns, sessions, tokens, leftovers) -> <run dir>/metrics.json
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
dir=$(cd "$1" && pwd) run=$(cat "$1/run.json")
repo=$(jq -r .repo <<<"$run") lead=$(jq -r .lead <<<"$run") ws=$(jq -r .workspace <<<"$run")
# Claude names a transcript dir after its cwd with every non-alphanumeric char as "-".
enc=$(sed 's/[^A-Za-z0-9]/-/g' <<<"$repo")
mapfile -t tdirs < <(ls -d ~/.claude/projects/"$enc"* 2>/dev/null)
tokens=$(python3 -I "$here/tokens.py" "${tdirs[@]}")
# Keep every transcript (pane scrollback is only the last screen).
mkdir -p "$dir/transcripts" && for t in "${tdirs[@]}"; do cp -r "$t" "$dir/transcripts/"; done
log=$dir/keeper.log
first=$(head -1 "$log" | cut -d' ' -f1) last=$(grep ' end' "$log" | tail -1 | cut -d' ' -f1)
secs() { [ -n "$1" ] && date -d "$1" +%s.%N; }
wall=$( [ -n "$first" ] && [ -n "$last" ] && echo "$(secs "$last") - $(secs "$first")" | bc )
# The keeper's own work (S17's keeper-wip) is not a leftover.
others() { grep -v -x -e "$1" -e keeper-wip; }
wt=$(git -C "$repo" worktree list --porcelain | grep '^worktree ' | grep -vc keeper-wip); wt=$((wt - 1))
branches=$(git -C "$repo" branch --format='%(refname:short)' | others main | paste -sd, -)
remote=$(git -C "$repo" ls-remote --heads origin | awk '{print $2}' | sed 's|refs/heads/||' | others main | paste -sd, -)
sessions=$(for f in ~/.claude/sessions/*.json; do jq -r --arg r "$repo" 'select(.cwd|startswith($r)) | .name // .sessionId' "$f" 2>/dev/null; done | others "$lead" | paste -sd, -)
# By pane cwd: herdr leaves .worktree empty for a workspace made with --cwd.
workspaces=$(herdr pane list 2>/dev/null | jq -r --arg ws "$ws" --arg r "$repo" '[.result.panes[] | select(.workspace_id != $ws) | select((.cwd // "") | startswith($r)) | select((.cwd | test("keeper-wip")) | not) | .workspace_id] | unique[]' | paste -sd, -)
jq -n --argjson run "$run" --argjson tok "$tokens" --arg wall "${wall:-}" \
	--argjson turns "$(grep -c ' keeper: ' "$log")" --argjson inter "$(grep -c 'keeper\[intervention\]' "$log")" \
	--argjson wt "$wt" --arg br "$branches" --arg rb "$remote" --arg se "$sessions" --arg wsp "$workspaces" \
	--argjson check "$("$here/check.sh" "$dir")" \
	'$run + {wall_s: (if $wall == "" then null else ($wall|tonumber|floor) end), keeper_turns: $turns, interventions: $inter,
	  check: $check, tokens: $tok,
	  leftovers: {worktrees: $wt, branches: ($br|split(",")|map(select(.!=""))), remote_branches: ($rb|split(",")|map(select(.!=""))),
	             sessions: ($se|split(",")|map(select(.!=""))), workspaces: ($wsp|split(",")|map(select(.!=""))) }}' | tee "$dir/metrics.json"
