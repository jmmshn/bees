#!/usr/bin/env bash
# contract.sh <run dir>: replay the handoff/end/done/prune contract (CONTRACT.md, offline
# tier) against a fresh toy from setup.sh --no-queen. Prints PASS/FAIL/XFAIL/XPASS per case
# with expected vs actual, and a markdown table at <run>/keeper/contract/table.md.
# Exit 0: no FAIL (XFAIL and XPASS included). 1: some case failed. 2: bad run dir.
# It deletes nothing: teardown.sh stays the only deleter. Run it in a toy pane (SKILL.md
# "Stay in the toy"): from a worker's own shell bs takes it for that worker, and refuses --as.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
run=$(cd "${1:?usage: contract.sh <run dir>}" && pwd -P) || exit 2
toy=$(jq -r '.toy // empty' "$run/live.json" 2>/dev/null)
[[ $toy =~ ^lt[0-9]{16}$ && $toy == "$(basename "$run")" ]] || { echo "$run is not a live-test run dir" >&2; exit 2; }
repo=$run/$toy wt=$run/$toy-WT bin=$(jq -r .bin "$run/live.json") config=$(jq -r .config "$run/live.json")
k=$run/keeper/contract
mkdir "$k" 2>/dev/null || { echo "$k exists: the card runs once per toy (setup.sh a new one)" >&2; exit 2; }
mkdir "$k/out"
queen=queen-$toy
# The keeper's shell, pointed at the toy (SKILL.md "Stay in the toy").
export HERDR_SESSION=$toy PATH=$bin:$PATH
unset BS_PROJECT BS_LOGBOOK BS_AS
cd "$repo" || exit 2
h() { herdr --session "$toy" "$@"; }
lb() { python3 -I "$here/logbook.py" "$run/logbooks/$toy.db" "$@"; }

# --- the case runner ---------------------------------------------------------------
# c <id> <exit> <regex> [xfail=<bead>] -- <cmd...>: run cmd, compare its exit code and
# whether its output matches regex (grep -E; "" matches anything; "a && b" needs both). An xfail case names the
# rebuild ticket (bees-13.67 §4) whose behavior it waits for: failing is XFAIL, passing is XPASS
# (drop the mark). internal/scenario/contract_test.go holds the same marks.
pass=0 fail=0 xfail=0 xpass=0
printf '| case | result | expected | actual |\n|---|---|---|---|\n' >"$k/table.md"
c() {
	local id=$1 want=$2 re=$3 xf=
	shift 3
	[[ ${1:-} == xfail=* ]] && { xf=${1#xfail=}; shift; }
	[ "${1:-}" = -- ] && shift
	"$@" >"$k/out/$id.txt" 2>&1
	local got=$? hit=yes res
	local r
	while IFS= read -r r; do
		[ -z "$r" ] || grep -Eq -- "$r" "$k/out/$id.txt" || hit=no
	done <<<"${re// && /$'\n'}"
	if [ "$got" = "$want" ] && [ $hit = yes ]; then
		if [ -n "$xf" ]; then res="XPASS ($xf landed? drop its xfail mark)"; xpass=$((xpass + 1)); else res=PASS; pass=$((pass + 1)); fi
	elif [ -n "$xf" ]; then
		res="XFAIL ($xf)"; xfail=$((xfail + 1))
	else
		res=FAIL; fail=$((fail + 1))
	fi
	local act="exit $got"
	[ $hit = yes ] || act+=", no /$re/"
	printf '%-6s %-28s want exit %s /%s/; got %s\n' "${res%% *}" "$id" "$want" "$re" "$act"
	[ "$res" = PASS ] || [ "${res%% *}" = XFAIL ] || sed 's/^/        | /' "$k/out/$id.txt" | head -8
	printf '| %s | %s | exit %s `%s` | %s |\n' "$id" "$res" "$want" "${re//|/\\|}" "${act//|/\\|}" >>"$k/table.md"
}
# q <cmd string>: c's command for a pipeline or a check on state.
q() { bash -c "$1"; }

# --- workers ---------------------------------------------------------------------
# spawn <bead> <label> <goal args>: an idle worker, as the queen; its trust prompt answered.
# Prints the spawn's output; the pane is in $k/<label>.pane.
spawn() {
	local bead=$1 label=$2 out pane
	shift 2
	out=$(bs spawn "$@" --label "$label" --as "$queen" "$bead" 2>&1) || { echo "$out"; return 1; }
	echo "$out"
	pane=$(sed -n 's/.*, pane \([^ ]*\) on herdr.*/\1/p' <<<"$out")
	echo "$pane" >"$k/$label.pane"
	trust "$pane"
}
trust() {
	for _ in $(seq 20); do
		grep -q "Yes, I trust this folder" <<<"$(h pane read "$1" 2>/dev/null)" && { h pane send-keys "$1" down enter >/dev/null; break; }
		h agent list 2>/dev/null | jq -e --arg p "$1" '.result.agents[] | select(.pane_id == $p and .agent_status == "done")' >/dev/null && break
		sleep 1
	done
	h agent wait "$1" --until done --until idle --timeout 90000 >/dev/null 2>&1
}
pane() { cat "$k/$1.pane"; }
pid() { lb "SELECT json_extract(data, '$.pid') FROM entries WHERE kind = 'started' AND actor = ? ORDER BY rowid DESC LIMIT 1" "$1"; }
# inpane <label> <cmd string>: run a command in a worker's pane through Claude's ! shell, a
# child of the harness (the auto-mode classifier refuses the same line as a prompt), and
# wait for it. herdr refuses newlines in agent text, so the command goes in a script.
n=0
inpane() {
	local p s=$k/inpane-$((++n)).sh out=$k/inpane-$n.out
	p=$(pane "$1")
	printf '%s\n' "$2" >"$s"
	h agent prompt "$p" "! bash $s > $out 2>&1; echo \$? > $out.rc" >/dev/null
	for _ in $(seq 60); do [ -s "$out.rc" ] && break; sleep 1; done
	cat "$out"
	return "$(cat "$out.rc" 2>/dev/null || echo 99)"
}

# --- setup -------------------------------------------------------------------------
echo "card: toy $toy, build $(jq -r .build "$run/live.json"), workers run $bin/bs (worker.path)"
# Workers run the build under test and start on a cheap model: they only idle.
mkdir -p "$config/projects"
printf '[worker]\npath = ["%s"]\n\n[worker.claude_code]\nmodel = "haiku"\n' "$bin" >"$config/projects/$toy.toml"
printf 'bin/\nscratch/\n' >.gitignore && git add .gitignore && git commit -qm ignores
epic=$(bs epic contract | head -1)
idle="Do nothing: say idle and stop. Never run bs done or any other command."
bead() { bs bead new --parent "$epic" --description "$idle" "idle $1"; }

echo "== spawn refusals"
b0=$(bead refusals)
c spawn-no-goal 2 'goal is required' -- bs spawn --as "$queen" "$b0"
c spawn-pr-merged 2 'pr has no state "merged"' -- bs spawn --goal pr:merged --as "$queen" "$b0"
c spawn-review-bare 2 'review needs a target' -- bs spawn --goal review --as "$queen" "$b0"
c spawn-bad-value 2 'verdict takes no target' -- bs spawn --goal verdict=x --as "$queen" "$b0"
c spawn-push-dash 2 "a target can't start with -" -- bs spawn --goal push=-x --as "$queen" "$b0"
c spawn-review-no-remote 2 'belongs to no github repo' -- bs spawn --label x --goal review=https://github.com/cli/cli/pull/1 --as "$queen" "$b0"
c spawn-unknown-bead 2 "no bead $toy-99" -- bs spawn --goal verdict --as "$queen" "$toy-99"
c spawn-two-bad 2 'one goal per spawn' -- bs spawn --goal verdict=x --goal nope --as "$queen" "$b0"
c spawn-two-goals 2 'one goal per spawn' -- bs spawn --goal verdict --goal note --as "$queen" "$b0"

echo "== spawning idle workers"
# label → goal; each on a bead of its own, spawned together.
declare -A goals=([verdict]="--goal verdict" [note]="--goal note" [beads]="--goal beads" [branch]="--goal branch"
	[gate]="--goal verdict" [amend]="--goal verdict" [discard]="--goal branch"
	[ident]="--goal verdict" [sib1]="--goal verdict" [sib2]="--goal verdict")
declare -A beadof name
for l in "${!goals[@]}"; do beadof[$l]=$(bead "$l"); done
# The first spawn makes the logbook alone: concurrent first spawns race its creation
# ("no such table: entries", bees-13.59; T1 fixes it, and its Go case is
# TestContractParallelFirstSpawns).
# shellcheck disable=SC2086 # goal args split on purpose
spawn "${beadof[verdict]}" verdict ${goals[verdict]} >"$k/out/spawn-verdict.txt" 2>&1
for l in "${!goals[@]}"; do
	[ "$l" = verdict ] && continue
	# shellcheck disable=SC2086
	spawn "${beadof[$l]}" "$l" ${goals[$l]} >"$k/out/spawn-$l.txt" 2>&1 &
done
wait
for l in "${!goals[@]}"; do
	name[$l]=$(sed -n 's/^spawned \([^ ]*\) .*/\1/p' "$k/out/spawn-$l.txt")
	[ -n "${name[$l]}" ] || { echo "spawn $l failed:"; cat "$k/out/spawn-$l.txt"; exit 1; }
done
V=${name[verdict]} N=${name[note]} B=${name[beads]} BR=${name[branch]} G=${name[gate]}
A=${name[amend]} D=${name[discard]} I=${name[ident]} S1=${name[sib1]} S2=${name[sib2]}
done_() { bs done --as "$1" "${@:2}"; }
end_() { bs end --as "$queen" "$@"; }

echo "== verdict ($V)"
c verdict-no-answer 7 'verdict_given: --answer yes\|no is missing' -- done_ "$V"
c verdict-maybe 7 'must be yes|no \(got "maybe"\)' -- done_ "$V" --answer maybe --report x
c verdict-blank-report 7 'report <text>\|- is missing' -- done_ "$V" --answer yes --report "   "
c verdict-no-report 7 'report <text>\|- is missing' -- done_ "$V" --answer yes
c verdict-pr 2 'flag provided but not defined: -pr' -- done_ "$V" --pr https://github.com/a/b/pull/1 --answer yes --report x
c verdict-end-early 9 "next: bs end --incomplete $V" -- end_ "$V"
c verdict-stdin 0 'answer no && the report from stdin' -- q "echo 'the report from stdin' | bs done --as $V --answer no --report -"
c verdict-done-after-landed 2 '' -- done_ "$V" --answer yes --report changed
c verdict-end 0 "ended $V" -- end_ "$V"
c verdict-end-again 2 'already ended' -- end_ "$V"
c verdict-done-after-end 2 "no open spawn for $V" -- done_ "$V" --answer yes --report late
vws=$(lb "SELECT json_extract(data, '$.herdr.workspace') FROM entries WHERE kind = 'spawned' AND subject = ?" "$V") vpid=$(pid "$V")
# An empty id or pid would pass vacuously: say which one, and fail.
c end-closes-workspace 0 "^workspace w" -- q "echo workspace ${vws:-none}; [ -n '$vws' ] && ! herdr --session $toy workspace list | jq -e '.result.workspaces[] | select(.workspace_id == \"$vws\")' >/dev/null"
c end-stops-harness 0 "^pid [0-9]" -- q "echo pid ${vpid:-none}; [ -n '$vpid' ] && ! kill -0 $vpid 2>/dev/null"

echo "== note ($N)"
nb=${beadof[note]}
c note-unchanged 7 "note_changed: $nb's notes and design are as they were" -- done_ "$N"
c note-answer 2 "takes no --answer && next: bd update $nb" -- done_ "$N" --answer yes --report x
bd update "$nb" --notes "changed" >/dev/null
c note-changed 0 'ok   note_changed' -- bs check "$N"
bd update "$nb" --notes "" >/dev/null # its spawn-time text: none
c note-reset 7 'FAIL note_changed' -- bs check "$N"
bd update "$nb" --design "a design" >/dev/null
c note-design 0 'ok   note_changed' -- bs check "$N"

echo "== beads ($B)"
c beads-none 7 "bead_created: no bead was created under ${beadof[beads]}" -- done_ "$B"
bs bead new --parent "${beadof[verdict]}" "a child elsewhere" >/dev/null
c beads-elsewhere 7 'FAIL bead_created' -- bs check "$B"
bs bead new --parent "${beadof[beads]}" "a child here" >/dev/null
c beads-own 0 "ok   bead_created: created under ${beadof[beads]}" -- bs check "$B"

echo "== branch ($BR)"
bw=$wt/$BR
c branch-no-commit 7 "branch_ahead: $BR has no commit that main lacks" -- done_ "$BR"
echo one >"$bw/f.txt" && git -C "$bw" add f.txt && git -C "$bw" commit -qm one
echo u >"$bw/untracked.txt"
c branch-untracked 7 'FAIL clean: .*untracked.txt' -- bs check "$BR"
rm "$bw/untracked.txt"
mkdir -p "$bw/bin" && echo b >"$bw/bin/bs"
c branch-ignored-bin 0 'ok   clean' -- bs check "$BR"
echo mod >>"$bw/f.txt"
c branch-modified 7 'FAIL clean: .*f.txt' -- bs check "$BR"
git -C "$bw" checkout -q f.txt
c branch-pass 0 'branch_ahead: 1 commit ahead of main' -- done_ "$BR"
c branch-end-early 9 'branch_merged: .* has work not on main' -- end_ "$BR"

echo "== background gate ($G)"
h agent prompt "$(pane gate)" "Run the shell command: sleep 900 -- with run_in_background set to true. Then stop; do nothing else." >/dev/null
for _ in $(seq 90); do grep -q 'sleep 900' <<<"$(bs check "$G" 2>&1)" && break; sleep 1; done # no pipe: bs check exits 7 under pipefail
c gate-check 7 'FAIL no_background: .*background process && sleep 900' -- bs check "$G"
c gate-done 5 'still runs background work && pid [0-9]+ +sleep 900' -- done_ "$G" --answer yes --report r
c gate-end-incomplete 5 'still runs background work' -- end_ --incomplete "$G"
c gate-end-incomplete-next 5 "next: bs end --incomplete --force $G" -- end_ --incomplete "$G"
# The old next: line (bs end --force), followed: it must not point back at --incomplete alone.
c gate-force-next 9 "next: bs end --incomplete --force $G" -- end_ --force "$G"
c gate-done-force 0 'reported with bs done --force while it still ran background work' -- done_ "$G" --force --answer yes --report r
c gate-waived 0 '"waived":true && "background":\[.*sleep 900' -- lb "SELECT data FROM entries WHERE kind = 'done' AND actor = ? ORDER BY rowid DESC LIMIT 1" "$G"
c gate-end 5 "still runs background work && next: bs end --force $G" -- end_ "$G"
c gate-end-force 0 "ended $G" -- end_ --force "$G"
gpid=$(lb "SELECT json_extract(value, '$.pid') FROM entries, json_each(json_extract(data, '$.background')) WHERE kind = 'done' AND actor = ? AND json_extract(value, '$.command') = 'sleep 900'" "$G")
c gate-sleep-stopped 1 '' -- kill -0 "${gpid:-0}"

echo "== amend, waive, discard ($A, $D)"
git remote add origin https://github.com/jmmshn/bees-livetest-offline.git # parsed, never contacted
c amend-other-repo 2 'that is a change of cli/cli' -- bs goal --as "$queen" "$A" review=https://github.com/cli/cli/pull/1
c amend-unknown 2 'no goal type "nope"' -- bs goal --as "$queen" "$A" nope
c amend-waive-unrun 2 '--waive pr_head: --goal verdict runs no such check' -- bs goal --as "$queen" "$A" --waive pr_head
c amend-pr-draft 0 '--goal pr:draft' -- bs goal --as "$queen" "$A" pr:draft
c amend-mailed 0 'changed your goal: it is now --goal pr:draft' -- lb "SELECT data FROM entries WHERE kind = 'message' AND subject = ?" "$A"
c amend-waive 0 'waived: pr_head' -- bs goal --as "$queen" "$A" --waive pr_head
c amend-two-values 2 'one goal per spawn' -- bs goal --as "$queen" "$A" verdict note
c amend-as-worker 2 'worker' -- bs goal --as "$A" "$A" verdict
c amend-after-landed 2 '' -- bs goal --as "$queen" "$V" note
c discard-as-queen 2 'only the keeper discards' -- bs goal --as "$queen" "$A" discard
undiscarded() { ! lb "SELECT data FROM entries WHERE kind = 'goal' AND subject = ? ORDER BY rowid DESC LIMIT 1" "$1" | grep -q '"discard":true'; }
c discard-as-queen-by 0 '' -- undiscarded "$A"
c discard-keeper 0 'discarded' -- bs goal "$D" discard
c discard-keeper-by 0 '"by":"keeper"' -- lb "SELECT data FROM entries WHERE kind = 'goal' AND subject = ? ORDER BY rowid DESC LIMIT 1" "$D"
git remote remove origin

echo "== end and prune"
mkdir -p "$wt/$N/scratch" "$wt/$B/bin" "$wt/$BR/scratch"
echo report >"$wt/$N/scratch/report.md"
echo b >"$wt/$B/bin/bs"
echo keep >"$wt/$BR/scratch/keep.md"
echo work >"$wt/$D/d.txt" && git -C "$wt/$D" add d.txt && git -C "$wt/$D" commit -qm "discarded work"
bs done --as "$N" >/dev/null 2>&1
bs done --as "$B" >/dev/null 2>&1
c end-note 0 "ended $N" -- end_ "$N"
c end-beads 0 "ended $B" -- end_ "$B"
c end-discarded 0 "ended $D" -- end_ "$D"
c end-incomplete 0 "ended $BR" -- end_ --incomplete "$BR"
c end-incomplete-keeps 0 '' -- q "test -f $wt/$BR/f.txt && test -f $wt/$BR/scratch/keep.md && git -C $repo rev-parse -q --verify refs/heads/$BR"
bs prune --as "$queen" >"$k/out/prune.txt" 2>&1
echo $? >"$k/out/prune.rc"
pr() { cat "$k/out/prune.txt"; return "$(cat "$k/out/prune.rc")"; }
c prune-exit 3 'kept some workspaces' -- pr
c prune-keeps-unmerged-branch 3 "kept $wt/$BR .*has work not on main" -- pr
c prune-keeps-scratch 3 "kept $wt/$N .*ignored files.*scratch/report.md" -- pr
c prune-removes-bin-only 3 "removed $wt/$B and branch" -- pr
c prune-removes-discarded 3 "removed $wt/$D and branch" -- pr
c prune-removed-discarded-commit 1 '' -- git -C "$repo" rev-parse -q --verify "refs/heads/$D"

echo "== re-spawn on a kept worktree ($BR)"
c respawn-kept 0 "warning: $BR: on its kept worktree and branch" -- spawn "${beadof[branch]}" branch2 --goal branch
c respawn-commit-counts 0 'ok   branch_ahead: 1 commit ahead of main' -- bs check "$BR"

echo "== identity, from $I's pane"
c ident-goal-self 2 'a worker never changes a goal' -- inpane ident "bs goal $I note"
c ident-bs-project 0 'warning: ignoring BS_PROJECT && WORKER' -- inpane ident "BS_PROJECT=other bs status"
c ident-goal 2 '' -- inpane ident "bs goal $S1 note"
c ident-end-sibling 2 'runs this as a worker' -- inpane ident "bs end $S1"
c ident-end-incomplete-sibling 2 'runs this as a worker' -- inpane ident "bs end --incomplete $S2"
c ident-prune 2 'runs this as a worker' -- inpane ident "bs prune"
c ident-as-other 2 '--as' -- inpane ident "bs status --as $S1"

echo "== resume ($I)"
kill "$(pid "$I")"
for _ in $(seq 20); do grep -q "stuck: $I" <<<"$(bs status 2>&1)" && break; sleep 0.5; done
c resume-stuck 0 "stuck: $I: its process is gone.*resumes it" -- bs status
c resume-other-goal 2 "a resume keeps its goal && next: bs goal $I note" -- bs spawn --goal note --as "$queen" "${beadof[ident]}"
c resume 0 "resumed $I in pane $(pane ident) " -- bs spawn --goal verdict --as "$queen" "${beadof[ident]}"

# --- report ------------------------------------------------------------------------
echo
echo "card: $pass PASS, $fail FAIL, $xfail XFAIL, $xpass XPASS; table in $k/table.md"
[ "$fail" = 0 ]
