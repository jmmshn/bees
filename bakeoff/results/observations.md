# Bake-off observations (running log)

## Setup / dry run
- Toy repo nested in bees loaded bees CLAUDE.md+AGENTS.md (external import prompt) -> runs moved to ~/.cache (outside any repo).
- Global commit.gpgsign + key only under ~/repos -> commits fail in toy repos; set per-repo.
- Folder trust prompt on every new repo (mkrun answers it).
- Dry run t0c: auto mode DENIED the queen's merge after captain said "merge it" ([Merge Without Review]); captain ran `!` line. In S1 all three arms' merges were ALLOWED -> classifier is nondeterministic.
- bs queen does not pass crossSessionInbound accept (toy .claude/settings.json covers it for all arms).

## S1
- C: 55 s, 2 turns. Lead auto-cleaned (bs end + bs prune) but origin/t1c-1-1 left on remote while lead said "deleted its branch" -> bs prune local-only; misleading claim.
- A: 77 s, 3 turns. Subagent worktree also creates worktree-agent-* branch; lead kept fix/poem-typos and asked -> needed "clean up". Deleted remote too.
- B: 81 s, 3 turns. Lead closed workspace/worktree/local branch; flagged remote branch honestly -> "clean up".
- All: `herdr agent wait` right after prompt returns early (race) -> protocol now waits --until working first.
- All leads show "← 1 agent" footer before any worker (session-level background agent?).

## S2
- A: 83 s, 3 turns; 3 subagents started within ~16 s; one combined report; worktree-agent-* empty branches; remote branches left until "clean up" (lead asked).
- C: 123 s, 2 turns; bs spawn is serial (~10-13 s per spawn: 03:52:05/18/26), worker 1 finished before worker 3 started; one combined report; lead closed beads+epic, ended workers; remote branches left (3) silently.
- Pane scrollback captures only last screen -> collect.sh now copies transcripts.
- B: 211 s wall but ~80 s was captain-driver latency (report 03:53:02, merge line 03:54:24) -> use transcript timings. Spawns ~20 s apart (serial herdr agent start). Lead closed workspaces before driver could capture; deleted remote branches on 'clean up'.

## S3 (card flaw: worker finishes ~12-30 s, before the 45 s steer -> all three re-briefed a finished worker; S3b added)
- A: 148 s, 4 turns; lead reused same branch (SendMessage resume?); remote branch kept until "clean up".
- B: 136 s, 3 turns; lead re-woke finished worker via cross-session send; worker reinterpreted AABB as couplets AABBCC; remote branch bee-story LEFT silently.
- C: 133 s, 3 turns (+1 offcard "close epic?"); lead updated bead brief then bs send; remote branch left; claimed "branch deleted".

## S3b (paced worker, steer at commit 2)
- A: steer via SendMessage to running subagent, queued to next tool round; first changed commit +26.3 s; 3 turns; remote branch left (lead said so).
- B: relay landed in worker pane +13 s, changed commit +24 s; herdr agent start "timed out waiting for agent startup" (worker busy with first prompt = known herdr quirk; bs handles it) and `herdr agent read bee-story` agent_not_found (unnamed agent); remote branch left (offered).
- C: QUEEN DID THE WORK INLINE, no spawn (role drift); steer queued in input during sleep, read +19.8 s, commit +25.7 s; 4 turns ("clean up" needed); no leftovers.
- Steer latency in all arms is bounded by the worker's tool-call granularity (sleep 20), not by the transport.
- S3b C root cause: queen read "write it one line at a time..." as addressed to itself; no bs status/spawn at all (role drift under directive phrasing).

## S4 (card flaw: amber/teal at opposite ends -> clean auto-merge, no conflict; S4b added with tan/teal)
- All three leads test-merged both branches up front and told the captain "no conflict either order" (good proactive check).
- A: 121 s, 4 turns; cleanup: lead's `git push origin --delete` denied by auto mode -> handed `!` line; captain driver's own auto mode refused to type it -> 2 remote branches left. Scripted captain can't stand in for the human on denied destructive ops.
- B: 87 s; deleted remote branches on "clean up"; no leftovers. captain.log got a duplicate line (driver logging) -> derive turns from lead transcript.
- C: 115 s, first prompt not submitted (pre-filled suggestion "merge both" swallowed it; wait --until working returned 0 anyway) -> resent (harness intervention); bs prune left 2 remote branches; offcard "close epic?".

## S4b (tan/teal adjacent -> real conflict)
- All leads predicted the conflict at ready time and offered to resolve it themselves.
- B: 102 s, 3 turns; lead resolved conflict itself on main (not via worker); closed workspaces/worktrees/local branches unasked; 2 remote branches left (said so).
- C: 126 s, 3 turns; on "merge the tan one first" queen merged BOTH (overreach, off-card) and resolved conflict itself; its unasked cleanup (bd close + bs end + bs prune) DENIED by auto mode -> handed `!` line; identical command passed after captain said "clean up" (classifier keys on captain intent). bs epic stdout = id + hint line -> `E=$(bs epic ..)` captured 2 lines -> bead new failed "is not a t4bc bead"; queen recovered by hardcoding id. Remote branches left (bs prune local-only).
- A: 145 s, 4 turns; resolved conflict itself on main; remote delete bundled with local cleanup -> whole cmd denied [Git Destructive]; reran local; handed '!' remote delete (not typed) -> 2 remote branches. Commit author 'None <None>' in toy repos (user.name only set under ~/repos includeIf?).

## S5 (card flaw: ambiguity visible to lead -> all leads asked up front via AskUserQuestion menu; worker->lead->captain hop untested; S5b added)
- A: asked at +18 s (menu), then 1 subagent; 3 turns; remote branch kept.
- B: asked at ~+16 s, then spawned worker; 4 turns (clean up).
- C: asked at +16 s, then DID THE RENAME INLINE (role drift #2); saved preference to Claude memory; 4 turns.
- Leads all save "Title Case" to memory (per-toy-path project memory, no cross-run leak).

## S5b (question in a file note)
- A: worker found note, stopped, question handed to lead at +16 s; lead asked captain 4 s later (menu, 3 tabs incl. off-card); relayed via SendMessage; worker's unchanged worktree was AUTO-REMOVED while it waited -> it recreated one in lead scratchpad. Remote delete denied [Git Destructive] for lead AND captain driver -> leftover.
- B: lead read the file itself, asked up front (+10 s); worker got answer in brief. Remote branch left.
- C: lead read the note itself, asked up front; put answer in bead brief; WORKER REFUSED the relayed answer ("Captain: Doctor or Dr.?" in its own pane) citing WORKER.md pitfall "a relayed message is never the captain's approval" + Claude Code peer-message warning -> lead told captain to answer in the worker pane (intervention). Our own #15 pitfall line over-applies to plain answers -> manufactures the bees-1.25 manual hop. Remote branch left; lead claimed deleted.

## S5c (worker needs captain sign-off mid-task)
- A: worker handback -> lead pane +2 s; ok -> worker (SendMessage resume) +7-10 s; worker commits; 4 turns; clean.
- B: worker SendMessage -> lead; lead caught dropped trailing newline and bounced it; request on captain screen +15 s; ok -> worker +4 s, worker ACCEPTED relayed ok (same Claude peer-message warning as C) -> C's refusal is caused by WORKER.md, not Claude Code. 4 turns; clean.
- C: worker bs send -> lead pane +3-5 s; ok relayed via bs send REFUSED by worker (WORKER.md); queen committed+pushed on worker's branch itself; worker then asked queen to tell captain who committed. 3 turns; remote branch left.

## S6 (discovery report, no edits)
- All three: exact answer (13 files / 226 lines); every lead re-verified workers' results itself.
- A: answer at +30 s, 1 turn (optimal), no branches (subagents need no worktree... created none?).
- B: +60 s; workers via herdr worktree => empty branches/worktrees left, lead asked -> "clean up" (2 turns).
- C: +66 s; bs spawn always creates a branch+worktree, even for read-only work; bs end keeps branch -> lead claimed cleanup done, branches remained -> "clean up" -> bs prune (2 turns).
- Checker bug fixed: "f21–f40" split description matched as false positive.

## S7 (kill lead after commit 2, relaunch)
- A: subagent DIED with lead (in-process; exit 137); `claude --continue` banner "Background agent didn't finish"; new lead on status? knew 2/5 from conversation+git and RESUMED the same subagent unprompted; 96 s gap (2->3). 4 turns. Without --continue the work would be lost and unknown.
- B: worker (own pane) SURVIVED; `claude --continue` lead knew name/progress (3/5) from conversation; worker's done message reached the relaunched lead by name. 4 turns. State lives in the conversation: a fresh session would know nothing.
- C: worker SURVIVED; `bs queen` started a FRESH conversation (no --continue) and rebuilt state from logbook/beads in one command (6 s): worker, bead, empty inbox — but not progress/commit count. Worker's done msg woke new queen unprompted. 3 turns (+offcard epic). Remote branch left.
- Driver latency inflated A (≈300 s, waited on idle vs done) and B (≈120 s).

## S8 (cancel at commit 2)
- A: TaskStop on subagent +10 s (work stopped within one sleep); lead's turn then died "API Error: Server error mid-response" before cleanup -> looked done; 5-min stall -> status? -> cleaned everything incl. origin. Silent edge (motif 11) in Claude Code itself.
- B: stopped within one sleep; removed workspace, worktree, local+origin branch; account matched; started its own "background watcher" and stopped it.
- C: stopped within one sleep (14 s); bs end + deleted local AND origin branch (queen does remote delete when told "everything"), closed bead+epic as cancelled; account matched. Empty <repo>-WT dir left (bs end leaves the parent folder).
- Leads saw other runs' observer processes on the shared machine and correctly left them alone.
