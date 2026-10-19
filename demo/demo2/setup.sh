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
for t in herdr bs bd claude jq git python3 plannotator curl node; do command -v "$t" >/dev/null || die "needs $t on PATH"; done
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

# plannotator keeps its first-visit dialogs' answers in the browser, per origin: one fixed port
# (above) and stage.sh's profile, warmed up here headless, keep them off camera.
prof=$HOME/.cache/bees-demo2/chrome
mkdir -p "$prof"
warm=$stage/warm.md && printf '# warm-up\n' >"$warm"
# Its drafts and records go under $stage too, so no take inherits another's note.
export PLANNOTATOR_DATA_DIR=$stage/plannotator
PLANNOTATOR_BROWSER=none PLANNOTATOR_PORT=19480 plannotator annotate "$warm" --gate --json >/dev/null 2>&1 &
pn=$!
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --user-data-dir="$prof" \
	--remote-debugging-port=9224 --no-first-run http://localhost:19480 >/dev/null 2>&1 &
chrome=$!
B='(t)=>[...document.querySelectorAll("button")].find(b=>b.innerText.trim()===t)'
for _ in $(seq 20); do node "$here/cdp.mjs" 9224 "!!($B)('Approve')" 2>/dev/null | grep -q true && break; sleep 0.5; done
sleep 1
node "$here/cdp.mjs" 9224 "($B)('Continue')?.click(), 0" >/dev/null 2>&1; sleep 1
node "$here/cdp.mjs" 9224 "($B)('Got it')?.click(), 0" >/dev/null 2>&1; sleep 1
node "$here/cdp.mjs" 9224 "($B)('Approve')?.click(), 0" >/dev/null 2>&1; sleep 1
kill "$pn" "$chrome" 2>/dev/null || true
wait "$pn" "$chrome" 2>/dev/null || true

# The session's panes inherit the server's environment: none of this shell's BS_*, CLAUDE_*
# or HERDR_* variables, and plannotator opens its page through stage.sh.
(cd "$repo" && env -i HOME="$HOME" USER="$USER" LOGNAME="$USER" SHELL="${SHELL:-/bin/zsh}" PATH="$PATH" \
	TERM=xterm-256color COLORTERM=truecolor LANG="${LANG:-en_US.UTF-8}" BEADS_DIR="${BEADS_DIR:-}" \
	PLANNOTATOR_BROWSER="$here/stage.sh" PLANNOTATOR_PORT=19480 PLANNOTATOR_DATA_DIR="$PLANNOTATOR_DATA_DIR" \
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
# The keeper's shell, for git merge: zsh with a plain prompt, none of the keeper's own rc files.
mkdir -p "$stage/zdot"
printf '%s\n' "PROMPT='%F{yellow}buzz%f \$ '" 'RPROMPT=' >"$stage/zdot/.zshrc"
kws=$(herdr workspace create --cwd "$repo" --label keeper --no-focus)
herdr pane run "$(jq -r .result.root_pane.pane_id <<<"$kws")" "exec env ZDOTDIR=$stage/zdot zsh -i" >/dev/null
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
