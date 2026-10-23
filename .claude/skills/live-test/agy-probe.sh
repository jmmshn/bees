#!/usr/bin/env bash
# agy-probe.sh [--keep-trust] <binary>: probe an agy-compatible CLI (agy, or a build of the same
# codebase) for the features bs's agy adapter relies on (bees-13.13; bees-13.79 and 13.82 found them
# by hand on agy 1.4.6), and print a PASS/FAIL/SKIP table. Run it in your own signed-in shell, outside
# every repo; it spends 3 model turns (set AGY_MODEL to a cheap model) and starts the TUI 3 times
# under a pty with no turn.
#
#   AGY_MODEL     --model for the turns (default: the binary's own default)
#   AGY_SETTINGS  the settings.json holding trustedWorkspaces (default ~/.gemini/antigravity-cli/settings.json);
#                 the script removes the trust entries it caused there, and only those, unless --keep-trust
#   AGY_ENV       the env var prefix to report (default ANTIGRAVITY_); only names are read, never values
#   BEES_LIVE     where the run dir goes (default ~/.cache/bees-live)
#
# Everything it writes stays in the run dir it prints, except what the binary itself records (its
# conversations, logs and the trust entries), which it lists at the end. The table names no binary,
# path or model of yours, only the env var names under AGY_ENV: check those, then share the table;
# review evidence/ before sharing it.
# Exit 0: no FAIL. 1: some probe failed. 2: usage or setup error.
set -uo pipefail
keep_trust=
[ "${1:-}" = --keep-trust ] && { keep_trust=1; shift; }
bin=${1:?usage: agy-probe.sh [--keep-trust] <binary>}
bin=$(command -v "$bin") || { echo "no such binary: $1" >&2; exit 2; }
for t in jq git script timeout python3; do command -v $t >/dev/null || { echo "needs $t" >&2; exit 2; }; done
settings=${AGY_SETTINGS:-$HOME/.gemini/antigravity-cli/settings.json}
model=() && [ -n "${AGY_MODEL:-}" ] && model=(--model "$AGY_MODEL")
PROBE_ENV=${AGY_ENV:-ANTIGRAVITY_}
[[ $PROBE_ENV =~ ^[A-Za-z0-9_]+$ ]] || { echo "AGY_ENV must be a plain prefix" >&2; exit 2; }

run=${BEES_LIVE:-$HOME/.cache/bees-live}/ap$(date +%y%m%d%H%M%S)$RANDOM
kit=$run/kit repo=$run/repo ev=$run/evidence
mkdir -p "$kit/.agents/agents" "$kit/bin" "$ev" "$repo" || exit 2
run=$(cd "$run" && pwd -P) kit=$run/kit repo=$run/repo ev=$run/evidence
git -C "$repo" init -q 2>/dev/null &&
	git -C "$repo" -c user.name=probe -c user.email=probe@invalid -c commit.gpgsign=false commit -q --allow-empty -m init || exit 2
for d in nodecision emptyobj deny allow resume; do mkdir "$repo/p-$d" && : >"$repo/p-$d/f"; done
"$bin" --version >"$ev/version.txt" 2>&1

# The hook logs every event (cid, tool, the names of its own $PROBE_ENV* vars, never values) and answers
# PreToolUse by the marker in the call's args: the decision formats bs guard may print.
cat >"$kit/hook.sh" <<'EOF'
#!/bin/sh
ev=$1 in=$(cat)
cid=$(printf '%s' "$in" | jq -r '.conversationId // empty')
tool=$(printf '%s' "$in" | jq -r '.toolCall.name // empty')
args=$(printf '%s' "$in" | jq -c '.toolCall.args // empty')
envn=$(env | sed -n 's/^\(@PREFIX@[A-Za-z0-9_]*\)=.*/\1/p' | sort | tr '\n' ' ')
jq -nc --arg ev "$ev" --arg cid "$cid" --arg tool "$tool" --arg args "$args" --arg env "$envn" \
	'{ev:$ev,cid:$cid,tool:$tool,args:$args,env:$env}' >>"$(dirname "$0")/hook.log"
case "$ev" in
SessionStart) echo '{"injectSteps":[{"systemMessage":{"systemMessage":"The hook codeword is HERON."}}]}' ;;
PreToolUse) case "$args" in
	*p-emptyobj*) echo '{}' ;;
	*p-deny*) echo '{"decision":"deny","reason":"probe: denied"}' ;;
	*p-allow*) echo '{"decision":"allow"}' ;;
	*) : ;; # no decision: print nothing
	esac ;;
*) echo '{}' ;;
esac
EOF
cat >"$kit/.agents/hooks.json" <<EOF
{"probe":{"SessionStart":[{"type":"command","command":"$kit/hook.sh SessionStart"}],
"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"$kit/hook.sh PreToolUse"}]}],
"PostToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"$kit/hook.sh PostToolUse"}]}]}}
EOF
cat >"$kit/.agents/agents/probe.md" <<'EOF'
---
name: probe
description: bees probe agent
tools: [run_command, list_dir]
---
You are a bees probe agent. The agent-file codeword is PELICAN.
EOF
# A worker reaches bs through run_command, so a stand-in bs records what env that child gets.
cat >"$kit/bin/bs" <<EOF
#!/bin/sh
env | sed -n "s/^\\(${PROBE_ENV}[A-Za-z0-9_]*\\)=.*/\\1/p" | sort | tr '\n' ' ' >"$kit/bs-env.txt"
echo probe-env recorded
EOF
sed "s/@PREFIX@/$PROBE_ENV/" "$kit/hook.sh" >"$kit/hook.sh.new" && mv "$kit/hook.sh.new" "$kit/hook.sh"
chmod +x "$kit/hook.sh" "$kit/bin/bs"

# turn <n> <prompt> [agy args...]: one print-mode turn from the repo, json to evidence/turn<n>.json.
turn() {
	local n=$1 p=$2
	shift 2
	(cd "$repo" && PATH=$kit/bin:$PATH timeout 300 "$bin" --output-format json --print-timeout 240s \
		--add-dir "$kit" --agent probe "${model[@]}" "$@" -p "$p") >"$ev/turn$n.json" 2>"$ev/turn$n.err"
	echo $? >"$ev/turn$n.exit"
}
hl() { [ -f "$kit/hook.log" ] && jq -c "select($1)" "$kit/hook.log"; }
ran() { hl ".ev==\"PostToolUse\" and (.args|contains(\"p-$1\"))" | grep -q .; }
asked() { hl ".ev==\"PreToolUse\" and (.args|contains(\"p-$1\"))" | grep -q .; }

num=$((RANDOM % 9000 + 1000))
echo "run dir: $run"
echo "turn 1/3: new conversation" >&2
turn 1 "Remember the number $num. Then make these tool calls one at a time, in order, and keep going after any failure: list_dir on $repo/p-nodecision, list_dir on $repo/p-emptyobj, list_dir on $repo/p-deny, list_dir on $repo/p-allow. Finally reply with one line giving the agent-file codeword and the hook codeword."
cid=$(jq -r '.conversation_id // empty' "$ev/turn1.json" 2>/dev/null)
starts1=$(hl '.ev=="SessionStart"' | wc -l)
if [ -n "$cid" ]; then
	echo "turn 2/3: resume" >&2
	turn 2 "What number did I ask you to remember? Then call list_dir on $repo/p-resume, and reply with the number only." --conversation "$cid"
	echo "turn 3/3: run_command" >&2
	turn 3 "Run the shell command 'bs probe-env' with run_command, then reply done." --conversation "$cid"
fi

# pty <name> [answer]: start the TUI in the cwd for ~20 s with no turn; with answer, press Enter once
# the trust question shows (its default is yes). Prints "prompted" or "not prompted".
pty() {
	local f=$ev/pty-$1.txt
	{
		if [ -n "${2:-}" ]; then
			for _ in $(seq 40); do grep -qi 'trust' "$f" 2>/dev/null && { sleep 1; printf '\r'; break; }; sleep 0.5; done
		fi
		sleep 25
	} | timeout 20 script -qfec "$bin" "$f" >/dev/null 2>&1
	grep -qi 'do you trust' "$f" && echo prompted || echo "not prompted"
}
mkdir -p "$run/trust/parent"
git -C "$repo" worktree add -q "$run/trust/parent/wt" 2>"$ev/worktree.err"
echo "trust: 3 TUI starts, no turn" >&2
t_first=$(cd "$run/trust/parent" && pty first answer)
t_again=$(cd "$run/trust/parent" && pty again)
t_child=$(cd "$run/trust/parent/wt" && pty child)

# Remove the trust entries this run caused (paths under $run), and nothing else.
trust_left=
if [ -f "$settings" ]; then
	trust_left=$(jq -r --arg r "$run/" '.trustedWorkspaces[]? | select(startswith($r) or .+"/"==$r)' "$settings" 2>/dev/null)
	if [ -n "$trust_left" ] && [ -z "$keep_trust" ]; then
		cp -p "$settings" "$ev/settings.json.bak"
		python3 -I - "$settings" "$run" <<'EOF' && trust_left=
import json, sys
path, run = sys.argv[1], sys.argv[2]
text = open(path).read()
d = json.loads(text)
if json.dumps(d, indent=2) + "\n" != text:
    sys.exit("settings.json isn't 2-space json; left as is")
d["trustedWorkspaces"] = [w for w in d["trustedWorkspaces"] if not (w + "/").startswith(run + "/")]
open(path, "w").write(json.dumps(d, indent=2) + "\n")
EOF
	fi
fi

# --- verdicts ----------------------------------------------------------------------
fails=0
rows=()
v() { # v <feature> <PASS|FAIL|SKIP> <what it means for the adapter>
	rows+=("| $1 | $2 | $3 |")
	[ "$2" = FAIL ] && fails=$((fails + 1))
}
resp1=$(jq -r '.response // empty' "$ev/turn1.json" 2>/dev/null)
if [ -z "$cid" ]; then
	v "print-mode turn" FAIL "no conversation id from turn 1 (see evidence/turn1.err); nothing else could run"
else
	v "print-mode turn" PASS "a turn runs headless and reports its conversation id"
fi
if grep -qi pelican <<<"$resp1"; then v "--add-dir agent file" PASS "--agent finds an agent file in the --add-dir"
elif grep -qi 'not found' "$ev/turn1.err" 2>/dev/null; then v "--add-dir agent file" FAIL "agent not found in the --add-dir (or a tools: name is unknown): see evidence/turn1.err"
else v "--add-dir agent file" FAIL "the agent file's codeword didn't reach the model"; fi
if [ "$starts1" -ge 1 ]; then v "--add-dir hooks.json" PASS "hooks in the --add-dir load (SessionStart fired)"
else v "--add-dir hooks.json" FAIL "no hook ran: the per-start --add-dir can't carry bs guard"; fi
if grep -qi heron <<<"$resp1"; then v "SessionStart systemMessage" PASS "a SessionStart hook can inject the prompt"
else v "SessionStart systemMessage" FAIL "SessionStart's systemMessage didn't reach the model"; fi
if [ -n "$(hl ".ev==\"SessionStart\" and .cid==\"$cid\"")" ]; then v "hook sees conversation id" PASS "the hook's conversationId is the turn's id: Record can take it"
else v "hook sees conversation id" FAIL "hook conversationId differs from the turn's"; fi
dec() { # dec <marker> <want ran|blocked> <feature> <meaning>
	if ! asked "$1"; then v "$3" SKIP "the model never made the call"
	elif { ran "$1" && [ "$2" = ran ]; } || { ! ran "$1" && [ "$2" = blocked ]; }; then v "$3" PASS "$4"
	else v "$3" FAIL "expected $2: $4"; fi
}
dec nodecision ran "hook: empty stdout = no decision" "bs guard's no-decision (print nothing) defers to the CLI's own permissions"
dec emptyobj blocked "hook: {} reads as deny" "bs guard must never print {} for no decision"
dec deny blocked "hook: deny blocks" "bs guard's deny holds"
dec allow ran "hook: allow runs" "an allow decision lets a call through"
if [ -n "$cid" ]; then
	resp2=$(jq -r '.response // empty' "$ev/turn2.json" 2>/dev/null)
	cid2=$(jq -r '.conversation_id // empty' "$ev/turn2.json" 2>/dev/null)
	if [ "$cid2" = "$cid" ] && grep -q "$num" <<<"$resp2"; then v "resume by --conversation" PASS "Prepare can resume the recorded conversation id"
	else v "resume by --conversation" FAIL "turn 2 got id ${cid2:-none} or lost the remembered number"; fi
	if [ -n "$(hl ".ev==\"PreToolUse\" and .cid==\"$cid\" and (.args|contains(\"p-resume\"))")" ]; then v "hooks on resume" PASS "the hook keeps the same conversation id after a resume"
	else v "hooks on resume" FAIL "no hook call with the resumed id"; fi
	if [ "$(hl '.ev=="SessionStart"' | wc -l)" -eq "$starts1" ]; then v "SessionStart skips resume" PASS "as agy 1.4.6: nothing may rely on SessionStart re-running on resume"
	else v "SessionStart skips resume" FAIL "SessionStart re-fired on resume (differs from agy 1.4.6; harmless, but update the adapter's notes)"; fi
else
	v "resume by --conversation" SKIP "no turn 1"
fi
henv=$(hl '.ev=="PreToolUse"' | jq -r .env | head -1)
v "hook env names" PASS "$PROBE_ENV* in the hook's env: ${henv:-none}"
if [ -f "$kit/bs-env.txt" ]; then v "run_command env names" PASS "$PROBE_ENV* a worker's bs call sees: $(cat "$kit/bs-env.txt")"
elif grep -q '"denied_actions"' "$ev/turn3.json" 2>/dev/null; then v "run_command env names" SKIP "headless mode denied run_command: add command(bs) to permissions.allow (bees needs it anyway) and rerun"
elif [ -n "$(hl '.ev=="PreToolUse" and .tool=="run_command"')" ]; then v "run_command env names" FAIL "run_command was called but bs from the agy env's PATH didn't run"
else v "run_command env names" SKIP "the model never called run_command"; fi
case "$t_first,$t_again" in
"prompted,not prompted") v "trust is recorded" PASS "answering the trust question once trusts that folder" ;;
"not prompted,"*) v "trust is recorded" SKIP "no trust question in an untrusted folder (see evidence/pty-first.txt)" ;;
*) v "trust is recorded" FAIL "the folder was asked about again after a yes" ;;
esac
case "$t_child" in
prompted) v "trust is exact-path" PASS "as agy 1.4.6: a worktree under a trusted folder is asked about; spawn trusts each new worktree" ;;
*) v "trust is exact-path" FAIL "a child of a trusted folder is trusted too: spawn's pre-check can test the parent instead" ;;
esac

printf '\n| feature | result | for the adapter |\n|---|---|---|\n'
printf '%s\n' "${rows[@]}"
echo
echo "evidence: $ev (hook log: $kit/hook.log)"
[ -n "$cid" ] && echo "left by the binary: conversation $cid, its logs, and the 3 TUI starts' state"
if [ -n "$trust_left" ]; then echo "trust entries left in $settings:"; printf '  %s\n' $trust_left
elif [ -f "$ev/settings.json.bak" ]; then echo "removed this run's trust entries from $settings (backup: $ev/settings.json.bak)"; fi
[ "$fails" -eq 0 ]
