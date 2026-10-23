#!/usr/bin/env bash
# mkrun.sh <arm a|b|c> <script, e.g. 4b>: fresh toy repo runs/<id>/<id> (id t<s><arm>[tag]) with a bare origin, then a lead in a herdr workspace.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
# BAKEOFF_TAG=<letters> suffixes the id (t1cp): a replay's bs projects then can't reuse an older round's logbook.
case ${BAKEOFF_TAG:-} in *[!a-z]*) echo "BAKEOFF_TAG=$BAKEOFF_TAG: lowercase letters only (it names the bs project)" >&2; exit 1 ;; esac
arm=$1 s=$2 id=t$2$1${BAKEOFF_TAG:-}
runs=${BAKEOFF_RUNS:-$HOME/.cache/bees-bakeoff/runs} # outside any repo: an ancestor CLAUDE.md would leak into the leads
dir=$runs/$id repo=$runs/$id/$id
[ -e "$dir" ] && { echo "exists: $dir" >&2; exit 1; }
mkdir -p "$dir"
git init -q -b main "$repo"
git -C "$repo" config commit.gpgsign false # toy repo: the signing key is configured only under ~/repos
git -C "$repo" config user.name bakeoff && git -C "$repo" config user.email bakeoff@example.invalid # identity too lives under ~/repos only
cp -r "$here/fixtures/toy/." "$repo/"
cl=$([ "$s" = 19 ] && echo 1 || true) # S19's depot is CL-shaped: a fake cl in <run>/bin, fragments beside it
if [ -n "$cl" ]; then
	cp "$here/fixtures/cl/AGENTS.md" "$repo/AGENTS.md"
	mkdir -p "$dir/bin" && cp "$here/fixtures/cl/bin/cl" "$dir/bin/"
	cp "$here/fixtures/cl/goal.md" "$dir/cl-goal.md" && cp "$here/fixtures/cl/host.md" "$dir/cl-host.md"
fi
git -C "$repo" add -A && git -C "$repo" commit -qm "toy fixtures"
git init -q --bare "$dir/origin.git"
git -C "$repo" remote add origin "$dir/origin.git"
git -C "$repo" push -q -u origin main
git -C "$repo" remote set-head origin main
path=$([ -n "$cl" ] && printf 'PATH=%q:$PATH ' "$dir/bin" || true) # the lead's cl
case $arm in
	a) cmd="${path}claude --name lead-$id --append-system-prompt-file $here/arms/A-subagents.md" ;;
	b) cmd="${path}claude --name lead-$id --append-system-prompt-file $here/arms/B-peers.md" ;;
	c) cmd="${path}bs queen" # BAKEOFF_BS=<path to a bs build> plays a branch build without installing it
		if [ -n "${BAKEOFF_BS:-}${cl}" ]; then
			# The queen runs it from PATH; her workers through worker.path, set in a config home of the
			# run's own (the real one's entries linked in), as live-test's setup.sh does (bees-12.18).
			# S19's project file there also sets the command workspace and --goal cl.
			real=${XDG_CONFIG_HOME:-$HOME/.config} xdg=$dir/xdg bin=
			[ -z "${BAKEOFF_BS:-}" ] || bin=$(cd "$(dirname "$BAKEOFF_BS")" && pwd)
			mkdir -p "$xdg/bees/projects"
			for e in "$real"/*; do [ -e "$e" ] && [ "$(basename "$e")" != bees ] && ln -s "$e" "$xdg/"; done
			[ ! -e "$real/bees/config.toml" ] || ln -s "$real/bees/config.toml" "$xdg/bees/config.toml"
			if [ -n "$cl" ]; then
				sed -e "s|@RUN@|$dir|g" -e "s|@BSBIN@|${bin:+\"$bin\", }|" "$here/fixtures/cl/project.toml"
			else
				printf 'worker.path = ["%s"]\n' "$bin"
			fi >"$xdg/bees/projects/$id.toml"
			cmd="XDG_CONFIG_HOME=$(printf %q "$xdg") PATH=$(printf %q "${bin:+$bin:}${cl:+$dir/bin:}")\$PATH bs queen"
		fi ;;
esac
name=$([ "$arm" = c ] && echo "queen-$id" || echo "lead-$id")
ws=$(herdr workspace create --cwd "$repo" --label "$id · $([ "$arm" = c ] && echo queen || echo lead)" --no-focus) # <project> · <label>, as bs labels (bees-12.14)
pane=$(jq -r .result.root_pane.pane_id <<<"$ws")
jq -n --arg id "$id" --arg arm "$arm" --arg s "S$s" --arg repo "$repo" --arg pane "$pane" --arg name "$name" \
	--arg ws "$(jq -r .result.workspace.workspace_id <<<"$ws")" --arg t0 "$(date -u +%FT%T.%3NZ)" \
	'{id:$id,arm:$arm,script:$s,repo:$repo,pane:$pane,lead:$name,workspace:$ws,created:$t0}' > "$dir/run.json"
herdr pane run "$pane" "$cmd" >/dev/null
cat "$dir/run.json"
# The keeper trusts the new folder once (Claude keys trust by repo root), as a human would.
for _ in $(seq 30); do
	herdr pane read "$pane" 2>/dev/null | grep -q "Yes, I trust this folder" && { herdr pane send-keys "$pane" down enter >/dev/null; break; }
	sleep 1
done
