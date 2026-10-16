#!/usr/bin/env bash
# summary.sh: one markdown table row per run from every runs/*/metrics.json.
set -euo pipefail
runs=${BAKEOFF_RUNS:-$HOME/.cache/bees-bakeoff/runs}
echo "| Run | Script | Arm | Pass | Wall (s) | Keeper turns | Interventions | Sessions | Subagents | Output tok | Cache-read tok | Leftovers |"
echo "|---|---|---|---|---|---|---|---|---|---|---|---|"
for m in "$runs"/*/metrics.json; do
	jq -r '[.id, .script, .arm, (if .check.pass then "✅" else "❌ " + (.check.failures|join("; ")) end), .wall_s, .keeper_turns, .interventions,
	  .tokens.sessions, .tokens.subagents, .tokens.output, .tokens.cache_read,
	  ([.leftovers | to_entries[] | select((.value|type)=="array" and (.value|length)>0 or (.value|type)=="number" and .value>0) | "\(.key): \(.value|if type=="array" then join(",") else tostring end)"] | join("; ") | if .=="" then "none" else . end)]
	  | "| " + (map(tostring)|join(" | ")) + " |"' "$m"
done
