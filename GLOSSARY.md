# bees

bees coordinates several harness sessions (Claude Code today) working on one codebase: a queen hands out work to workers and they report back, all through a shared logbook.

## Work

**Project**:
The codebase a queen and its workers work on, named in one word (`pymatgen-analysis-defects` → `pymatgenanalysisdefects`).
_Avoid_: repo, rig, workspace

**Workspace**:
The directory a worker works in and the herdr workspace opened on it: its own git worktree, a directory the keeper's `create` command makes (`command`: a jj workspace, a CitC client), or a shared directory (`none`). bs works out which from the project (ADR-0010).
_Avoid_: sandbox, clone. Not the project's own repo (see Project)

**Goal**:
What a worker owes the queen, fixed at spawn (`--goal pr`, `pr:draft`, `review=<url>`, `verdict`, …): what it hands off, and the state that completes it (ADR-0010).
_Avoid_: contract, output type, out, deliverable, done kind

**Handoff**:
A worker's `bs done`: it names its PRs (or gives a verdict) and bs checks them against its goal; for a note or beads goal, bs finds the change on the bead itself.
_Avoid_: done report, delivery

**Complete**:
A goal whose state has been reached, a merged PR by default; bs finds it and records it as `landed`. Only then may the bead close.
_Avoid_: landed (as a verb the keeper types), finished, merged (for non-PR goals)

**Review host**:
Where a project's changes (pull requests, CLs) are reviewed and land, set by `review.host`: GitHub by default. bs reads PR goals' evidence (state, CI, comments) only through it (ADR-0010 Amendment 1).
_Avoid_: forge, remote, GitHub (for the general idea)

**Workspace commit**:
A commit made in a worker's workspace since it was first spawned there: one the worker wrote there, as its VCS records it (git: the HEAD reflog's commit, amend, merge-commit and revert entries; never a pull, checkout, reset, fast-forward, cherry-pick or rebase), or a rebased copy of one, matched by patch-id (a pull --rebase, a rebase -i, the host's "Update branch"). Work pulled in is never one. A handed-off change must hold one.
_Avoid_: own commit, worker commit

**Pending**:
A handoff check not failed but not passed yet: CI still running, or the review host not yet showing a push. Only pending left is exit 10, and `bs done --wait` waits it out.
_Avoid_: waiting, in progress

**Bead**:
One unit of work in the shared tracker, named `<project>-N[.M…]`.
_Avoid_: issue, ticket, task

**Epic**:
A project's top-level bead, `<project>-N`; every other bead of the project sits under one.
_Avoid_: milestone, story

## People and sessions

**Harness**:
The program a session runs in (Claude Code, omp, agy). One per machine, picked by the global `harness` key; bs reaches it only through its harness adapter; it is live while the process bs recorded at its start runs (ADR-0010).
_Avoid_: agent, model, runtime

**Keeper**:
The human who directs the work and talks to the queen; tends the hive and harvests its work. Replaces the earlier term "captain".
_Avoid_: captain, beekeeper, user, operator, admin

**Queen**:
The one coordinating session that assigns work to workers and reports to the keeper.
_Avoid_: orchestrator, manager, lead, coordinator

**Worker**:
A session the queen starts to own one workspace and one bead. Replaces the earlier term "bee"; the tool keeps the name bees (worker bees).
_Avoid_: bee (as a role), agent, subagent, crew member

**Session**:
One running harness process, known by its name (`queen-<project>`, or for a worker its bead id with `.` → `-`: `bees-2-1`).
_Avoid_: pane, agent, instance

## The logbook

**Logbook**:
The shared, append-only record of every message and fact in bees, and the only source of truth.
_Avoid_: ledger, event log, bus, database

**Entry**:
One row in the logbook; never changed or deleted once written.
_Avoid_: event, record, log line

**Message**:
An entry addressed from one session to another.
_Avoid_: event, notification, mail

**Receipt**:
An entry recording that a session has been shown a message.
_Avoid_: ack, read marker, cursor

**Unread**:
The messages addressed to a session that have no receipt from it.
_Avoid_: pending, backlog, queue

**Inbox**:
A session's unread messages as shown by `bs inbox`.
_Avoid_: mailbox, queue

## Waking sessions

**Doorbell**:
A short prompt sent into a session to tell it it has unread messages; it carries no message bodies.
_Avoid_: notification, ping, wake event

**Ring**:
To send a doorbell to a session.
_Avoid_: notify, wake, poke

## Outside names

**crew**:
The author's earlier, private multi-session tool that bees replaces. Docs cite its commits (`crew 4f65cd8`) as the incidents a design or bake-off card came from; the repo isn't public.

**omp**:
Another coding-agent harness the author uses (oh-my-pi); its bs adapter is bees-12.8 (oh_my_pi harness adapter).

**agy**:
Google Antigravity's CLI, a harness the keeper uses; probed against the harness seams in bees-12.7 (agy probe); its adapter is bees-12.12 (agy harness adapter).
