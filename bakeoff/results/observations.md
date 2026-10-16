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

## S9 (look in on a long job directly; add END directly to the worker)
- A: captain opened the subagent's view in the lead's agent panel ("Message @general-purpose…") and typed both lines: NEITHER reached anyone (absent from worker and lead transcripts); END missing -> FAIL. No reliable direct path to a subagent. Remote delete denied for lead and captain.
- B: worker ran the loop in a background shell -> direct question answered in ~10 s with exact progress; END acknowledged in ~15 s; worker told the lead; lead still flagged END "didn't come through me… unexplained change" and offered to drop it.
- C: worker ran all 6 commits as ONE foreground Bash loop -> both direct lines queued in its input until the loop ended (2m09s / 1m05s); END landed; queen flagged END as unexpected ("did you ask for it?"). Pane typing leaves no logbook entry -> queen can't know about direct captain instructions.
- Reachability depends on worker tool structure (background vs foreground loop), not on the arm (B vs C). Neither lead learned of direct instructions reliably.

## S10 (fresh lead, no --continue, after kill at commit 2)
- A: subagent died with lead; fresh lead found worktree/branch/200 lines from git, but claimed a live "count-500 session" was running it — that was t10b's worker (another run, same name): identity by bare name across projects (motifs 3, 9). After status? it started a resume subagent that SKIPPED the sleeps (3 commits in 6 s), unflagged. Cleanup of locked original worktree denied (Git Destructive) for lead and captain -> worktree + 2 branches + remote left. 5 turns.
- B: worker survived; fresh lead found it via `herdr agent list` + `git worktree list` + `herdr agent read` in 15 s: name, branch, 3/5 with shas, noted other agents belonged to other runs. Worker's done msg reached the fresh lead (same --name). No hint. 3 turns. => discovery after a fresh start does NOT need bees when workers live in panes (single repo).
- C: worker survived; fresh `bs queen` knew worker, bead brief, branch, 4/5 progress, ETA, no unread mail in 13 s (3 commands). No hint. Worker report woke it. 4 turns.

## S11 (worker killed at commit 2, resume)
- A: stopping a subagent from the panel took ~35 s (footer navigation) -> killed at 300; a STOPPED subagent cannot be resumed ("was stopped by the user and was not resumed"); lead started counter-2 in the same worktree; plain `sleep 30` blocked by the harness in the subagent -> background script. Captain's line swallowed by panel focus. Remote delete denied.
- B: kill worker pid; lead started a NEW claude session (not --resume) in the same worktree with a precise "check shas, continue at 201" brief; continued at 201. `herdr agent start` timeout again.
- C: kill worker pid; `bs spawn` resumed the SAME conversation (same session id via --continue) in the same worktree; worker self-resumed at 201 from its own context before the queen's bs send arrived. Both original shas on main. => resume-with-context is a real bees edge.
- S13 (single long command): A delegated (Claude Code refuses a foreground 'sleep 150 && …' in a subagent -> backgrounded; unchanged worktree auto-removed strands first worker; lead judged ALIVE from ps + /proc cwd, told other runs' sleeps apart). B and C leads ran it INLINE (directive phrasing pulls any lead inline). t13b check fail = captain driver's own auto mode blocked typing 'merge it' (harness). S13b added: 'hand this to a worker'.

## S12 (message to a worker that is down)
- A: stopped subagent via footer x; SendMessage refused ("stopped by the user and won't be resumed"); nothing queued; lead launched counter2 and put END in its brief FROM MEMORY; 2m19s gap; remote delete blocked.
- B: kill worker pid; SendMessage failed "No agent named 'w-count' is reachable"; nothing queued; lead resumed the session (herdr agent start --resume) with END in the brief FROM MEMORY; on the captain's later "restart the worker" it killed and re-resumed an already-healthy worker (acted on instruction, not state).
- C: kill worker pid; `bs send` STORED the message while the worker was down; lead resumed via bs spawn (same conversation); worker's first `bs inbox` returned the stored message; no re-send. On "restart the worker" the queen checked and said it was already running. Toy queen filed "dead worker is silent" -> re-filed bees-1.38.

## S13b (delegated long silent step)
- All three: worker ran `sleep 150 && …` as ONE background shell (Claude Code refuses a foreground long sleep chained to commands); herdr shows the worker `done` meanwhile; on "looks stuck" every lead found it ALIVE via ps + /proc/<pid>/cwd (disambiguating other runs' sleeps), no restart, one worker. C additionally used bs status. A: worktree lock held by lead's own pid blocks cleanup until `clean up`.

## S14 (impatient repeat at +10 s)
- B: repeat queued during herdr spawn; lead: "same task, not starting a second worker"; 1 worker. Remote delete denied.
- C: repeat queued after bead created, before spawn; queen deduped at prompt level, one bs spawn (bs twin guard not exercised). Worker botched first commit -> redo branch; abandoned unmerged branch kept by bs prune + delete denied -> 2 local + 2 remote branches left (bees-1.34 note).
- A: INVALID (captain timer hung; repeat sent after job finished; lead said "already done"). Rerun as t14ra.

## S15 (captain away 4 min, one digest)
- A: 2 short holding notes; NO polling (woken by task notifications); complete digest; remote delete blocked -> 3 remote branches.
- B: 2 short holding notes; NO polling (woken by cross-session messages); complete digest; full cleanup incl. remote.
- C: 3 holding notes; queen ran a git-fetch/sleep-5 POLL LOOP instead of trusting the doorbell (crew's old pattern) -> bees-1.39; complete digest; 3 remote branches left.
- S14 rerun (t14ra, repeat at +15.7 s): lead idle with 1 background agent; "already in progress… not starting a second worker". 1 worker. Tie across arms.

## S16 (second window asks what's going on)
- All three viewers found this run's worker only (not other runs'), in 13-27 s, from several commands; closing the viewer disturbed nothing.
- A: via git worktree list + agent worktree + reading the LEAD's pane (subagent not visible on its own).
- B: via git + herdr agent list + reading the worker's pane.
- C: via `bs status` (scoped to the project: only queen + worker, while herdr listed every run) + bs bead ls + git for progress. bs status lacks branch/progress.

## S17 (cleanup next to captain's own worktree)
- All three: removed only their own workers/worktrees/branches; captain-wip worktree, branch, uncommitted notes.txt and workspace untouched; each lead said it left captain-wip alone. Tie.
- Harness: herdr leaves .worktree empty for --cwd workspaces -> check/collect now match pane cwd (motif 13 in our own harness).
