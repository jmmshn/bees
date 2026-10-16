# bees

bees coordinates several Claude Code sessions working on one codebase: a queen hands out work to bees and they report back, all through a shared logbook.

## Work

**Project**:
The codebase a queen and its bees work on, named in one word (`pymatgen-analysis-defects` → `pymatgen_analysis_defects`).
_Avoid_: repo, rig, workspace

**Bead**:
One unit of work in the shared tracker, named `<project>-N[.M…]`.
_Avoid_: issue, ticket, task

**Epic**:
A project's top-level bead, `<project>-N`; every other bead of the project sits under one.
_Avoid_: milestone, story

## People and sessions

**Captain**:
The human who directs the work and talks to the queen.
_Avoid_: user, operator, admin

**Queen**:
The one coordinating Claude Code session that assigns work to bees and reports to the captain.
_Avoid_: orchestrator, manager, lead, coordinator

**Bee**:
A worker Claude Code session that owns one worktree and one bead.
_Avoid_: worker, agent, subagent, crew member

**Session**:
One running Claude Code process, known by its name (`queen-<project>`, or for a bee its bead id with `.` → `-`: `bees-2-1`).
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
