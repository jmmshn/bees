# bees

bees coordinates several Claude Code sessions working on one codebase: a queen hands out work to workers and they report back, all through a shared logbook.

## Work

**Project**:
The codebase a queen and its workers work on, named in one word (`pymatgen-analysis-defects` → `pymatgenanalysisdefects`).
_Avoid_: repo, rig, workspace

**Bead**:
One unit of work in the shared tracker, named `<project>-N[.M…]`.
_Avoid_: issue, ticket, task

**Epic**:
A project's top-level bead, `<project>-N`; every other bead of the project sits under one.
_Avoid_: milestone, story

## People and sessions

**Keeper**:
The human who directs the work and talks to the queen; tends the hive and harvests its work. Replaces the earlier term "captain".
_Avoid_: captain, beekeeper, user, operator, admin

**Queen**:
The one coordinating Claude Code session that assigns work to workers and reports to the keeper.
_Avoid_: orchestrator, manager, lead, coordinator

**Worker**:
A Claude Code session the queen starts to own one worktree and one bead. Replaces the earlier term "bee"; the tool keeps the name bees (worker bees).
_Avoid_: bee (as a role), agent, subagent, crew member

**Session**:
One running Claude Code process, known by its name (`queen-<project>`, or for a worker its bead id with `.` → `-`: `bees-2-1`).
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
Another coding-agent harness the author uses; v1 plans a bs adapter for it beside Claude Code.
