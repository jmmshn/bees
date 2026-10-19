#!/usr/bin/env bash
# setup.sh: the toy for demo 2 (RUN.md). Makes ~/demo/buzz, a git repo whose main holds
# buzz.py, starts a herdr session of its own (buzz) headless with a clean environment, and
# runs the toy's queen (bs queen) in it. The keeper then attaches: herdr session attach buzz.
#
# Everything it makes is named in teardown.sh, which removes exactly that. It refuses to run
# over leftovers of an earlier run: run teardown.sh first.
set -euo pipefail
unset BS_LOGBOOK BS_PROJECT # the toy's sessions find their logbook and project from its repo
here=$(cd "$(dirname "$0")" && pwd)
session=buzz
die() { echo "setup: $*" >&2; exit 1; }
for t in herdr bs bd claude jq git python3 plannotator curl; do command -v "$t" >/dev/null || die "needs $t on PATH"; done
[[ $HOME == /* ]] || die "HOME=$HOME is not absolute"
root=$HOME/demo repo=$HOME/demo/buzz stage=$HOME/demo/.stage
store=$HOME/.local/share/bees

# The toy starts bare: no repo, logbook, beads or herdr session from an earlier run.
for p in "$repo" "$repo-WT" "$stage" "$store/buzz.db"; do
	[ ! -e "$p" ] || die "$p exists: run teardown.sh first"
done
n=$(bd query --all --limit 0 --json 'id="buzz-*"' | jq length) || die "bd query failed"
[ "$n" = 0 ] || die "$n buzz-* beads exist: run teardown.sh first"
! herdr session list | awk -v s="$session" '$1 == s' | grep -q . || die "herdr session $session exists: run teardown.sh first"

mkdir -p "$root" "$stage"
git init -q -b main "$repo"
git -C "$repo" config commit.gpgsign false # the signing key and identity live under ~/repos only
git -C "$repo" config user.name keeper && git -C "$repo" config user.email keeper@example.invalid
cp "$here/buzz.py" "$repo/buzz.py"
git -C "$repo" add buzz.py && git -C "$repo" commit -qm "buzz.py: a bee and a flower"

# The session's panes inherit the server's environment: none of this shell's BS_*, CLAUDE_*
# or HERDR_* variables, and plannotator opens its page through stage.sh.
(cd "$repo" && env -i HOME="$HOME" USER="$USER" LOGNAME="$USER" SHELL="${SHELL:-/bin/zsh}" PATH="$PATH" \
	TERM=xterm-256color COLORTERM=truecolor LANG="${LANG:-en_US.UTF-8}" BEADS_DIR="${BEADS_DIR:-}" \
	PLANNOTATOR_BROWSER="$here/stage.sh" \
	nohup herdr --session "$session" server </dev/null >"$stage/herdr-server.log" 2>&1 &)
sock=
for _ in $(seq 40); do
	sock=$(herdr session list | awk -v s="$session" '$1 == s && $2 == "running" { print $NF }')
	[ -n "$sock" ] && HERDR_SOCKET_PATH=$sock herdr workspace list >/dev/null 2>&1 && break
	sleep 0.5
done
[ -n "$sock" ] || die "herdr session $session didn't start; see $stage/herdr-server.log"
export HERDR_SOCKET_PATH=$sock

old=$(herdr workspace list | jq -r '.result.workspaces[].workspace_id')
ws=$(herdr workspace create --cwd "$repo" --label queen --focus)
pane=$(jq -r .result.root_pane.pane_id <<<"$ws")
for w in $old; do herdr workspace close "$w" >/dev/null; done
herdr workspace create --cwd "$repo" --label keeper --no-focus >/dev/null # the keeper's shell, for git merge
herdr pane run "$pane" "bs queen" >/dev/null

# Trust the new folder once, as a human would; workers' worktrees inherit it. The prompt can
# draw before Claude reads keys (bees-8.1), so send them until it's gone.
seen=
for _ in $(seq 30); do
	if herdr pane read "$pane" 2>/dev/null | grep -q "Yes, I trust this folder"; then
		seen=1
		herdr pane send-keys "$pane" down enter >/dev/null
	elif [ -n "$seen" ]; then
		break
	fi
	sleep 2
done
herdr pane read "$pane" 2>/dev/null | grep -q "Yes, I trust this folder" && die "the folder-trust prompt is still up in $pane; answer it there"
echo "ready: queen-buzz in pane $pane of herdr session $session (socket $sock)"
echo "keeper: herdr session attach $session"
