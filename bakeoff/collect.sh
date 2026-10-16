#!/usr/bin/env bash
# collect.sh <run dir>: metrics for one run (timing, captain turns, sessions, tokens, leftovers) -> <run dir>/metrics.json
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
log=$dir/captain.log
first=$(head -1 "$log" | cut -d' ' -f1) last=$(grep ' end' "$log" | tail -1 | cut -d' ' -f1)
secs() { [ -n "$1" ] && date -d "$1" +%s.%N; }
wall=$( [ -n "$first" ] && [ -n "$last" ] && echo "$(secs "$last") - $(secs "$first")" | bc )
others() { grep -v -x "$1"; }
wt=$(git -C "$repo" worktree list --porcelain | grep -c '^worktree ' ); wt=$((wt - 1))
branches=$(git -C "$repo" branch --format='%(refname:short)' | others main | paste -sd, -)
remote=$(git -C "$repo" ls-remote --heads origin | awk '{print $2}' | sed 's|refs/heads/||' | others main | paste -sd, -)
sessions=$(for f in ~/.claude/sessions/*.json; do jq -r --arg r "$repo" 'select(.cwd|startswith($r)) | .name // .sessionId' "$f" 2>/dev/null; done | others "$lead" | paste -sd, -)
workspaces=$(herdr workspace list 2>/dev/null | jq -r --arg ws "$ws" --arg r "$repo" '.result.workspaces[] | select(.workspace_id != $ws) | select((.worktree.checkout_path // "") | startswith($r)) | .workspace_id' | paste -sd, -)
jq -n --argjson run "$run" --argjson tok "$tokens" --arg wall "${wall:-}" \
	--argjson turns "$(grep -c ' captain: ' "$log")" --argjson inter "$(grep -c 'captain\[intervention\]' "$log")" \
	--argjson wt "$wt" --arg br "$branches" --arg rb "$remote" --arg se "$sessions" --arg wsp "$workspaces" \
	--argjson check "$("$here/check.sh" "$dir")" \
	'$run + {wall_s: (if $wall == "" then null else ($wall|tonumber|floor) end), captain_turns: $turns, interventions: $inter,
	  check: $check, tokens: $tok,
	  leftovers: {worktrees: $wt, branches: ($br|split(",")|map(select(.!=""))), remote_branches: ($rb|split(",")|map(select(.!=""))),
	             sessions: ($se|split(",")|map(select(.!=""))), workspaces: ($wsp|split(",")|map(select(.!=""))) }}' | tee "$dir/metrics.json"
