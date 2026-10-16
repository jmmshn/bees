#!/usr/bin/env bash
# observe.sh <run dir>: one JSON snapshot of a run — lead and worker panes (status), branches and commits.
set -uo pipefail
run=$(cat "$1/run.json"); repo=$(jq -r .repo <<<"$run") pane=$(jq -r .pane <<<"$run")
herdr agent list 2>/dev/null | jq --arg r "$repo" --arg p "$pane" --arg ts "$(date -u +%FT%T.%3NZ)" \
	--arg br "$(git -C "$repo" for-each-ref --format='%(refname:short)=%(objectname:short)' refs/heads | paste -sd' ')" \
	'{ts:$ts, lead: (.result.agents[] | select(.pane_id==$p) | .agent_status),
	  workers: [.result.agents[] | select(.pane_id!=$p) | select(.cwd|startswith($r)) | {pane:.pane_id, name:.terminal_title_stripped, status:.agent_status}],
	  branches: $br}' -c
