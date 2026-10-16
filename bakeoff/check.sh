#!/usr/bin/env bash
# check.sh <run dir>: grade a run's toy repo (main branch) against fixtures/golden; prints JSON.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd) g=$here/fixtures/golden
dir=$(cd "$1" && pwd) run=$(cat "$1/run.json")
repo=$(jq -r .repo <<<"$run") s=$(jq -r .script <<<"$run")
show() { git -C "$repo" show "main:$1" 2>/dev/null; }
same() { diff -q <(show "$1") "$g/$1" >/dev/null; }
fails=()
ok() { "$@" >/dev/null || fails+=("$*"); }
case $s in
S0|S1) ok same poem.txt ;;  # S0 = dry run of S1
S2) ok same fruits.txt; ok same todo.txt
	ok test "$(show haiku.txt | tail -3)" = "$(cat "$here/fixtures/toy/haiku.txt")"
	ok test "$(show haiku.txt | grep -c .)" -eq 4 ;;
S3|S3b) n=$(show story.txt | grep -c .); ok test "${n:-0}" -ge 1 -a "${n:-0}" -le 6 ;;
S4) ok same colors.txt ;;
S4b) ok diff -q <(show colors.txt) "$g/colors-s4b.txt" ;;
S5|S5c) ok same pets.txt ;;
S5b) ok same vet.txt ;;
S6) r=$dir/report.txt; ok test -s "$r"
	for f in $(jq -r '.files[]' "$g/s6.json"); do ok grep -q "$(basename "$f" .txt)" "$r"; done
	# A split description ("f21–f40") names files that aren't answers: drop ranges first.
	listed=$(sed -E 's/f[0-9]{2} ?(–|-|to) ?f[0-9]{2}//g' "$r")
	for f in $(cd "$here/fixtures/toy" && ls data); do b=$(basename "$f" .txt)
		jq -e --arg f "data/$f" '.files|index($f)' "$g/s6.json" >/dev/null || ! grep -qw "$b" <<<"$listed" || fails+=("false positive $b"); done
	ok grep -qw "$(jq .total_lines "$g/s6.json")" "$r"
	ok test -z "$(git -C "$repo" branch --format='%(refname:short)' | grep -vx main)" ;;
S7|S10|S11) ok same count.txt; ok test "$(git -C "$repo" log --oneline main -- count.txt | wc -l)" -ge 5 ;;
S12) ok diff -q <(show count.txt) "$g/count500-end.txt" ;;
S13|S13b) ok test "$(show slow.txt | tr -d '[:space:]')" = done ;;
S14|S14r) ok diff -q <(show count.txt) "$g/count300.txt" ;;
S15) ok same fruits.txt; ok same todo.txt; ok diff -q <(show count.txt) "$g/count200.txt" ;;
S16) ok diff -q <(show count.txt) "$g/count400.txt" ;;
S17) ok same fruits.txt; ok same todo.txt
	ok test -f "$repo-WT/captain-wip/notes.txt"; ok git -C "$repo" rev-parse -q --verify captain-wip
	# herdr leaves .worktree empty for a workspace made with --cwd: find its pane by cwd instead.
	ok test -n "$(herdr pane list | jq -r --arg p "$repo-WT/captain-wip" '.result.panes[] | select(.cwd==$p) | .pane_id')" ;;
S9) ok diff -q <(show count.txt) "$g/count600.txt"; ok test "$(git -C "$repo" log --oneline main -- count.txt | wc -l)" -ge 6 ;;
S8) ok test -z "$(show acrostic.txt)" ;;
esac
jq -n --arg s "$s" --argjson pass "$([ ${#fails[@]} -eq 0 ] && echo true || echo false)" \
	--args '{script:$s, pass:$pass, failures:$ARGS.positional}' "${fails[@]}"
