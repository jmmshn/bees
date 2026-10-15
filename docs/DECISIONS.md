# Decisions

Every design decision for peons, from the design grill (beads-995, October 2026). Hard-to-reverse decisions also have an ADR in [adr/](adr/); behaviour is specified in [SPEC.md](SPEC.md); words are defined in [GLOSSARY.md](../GLOSSARY.md). Ids (S, R, M) match the decisions page used during the grill.

Status: **accepted**, **superseded** (replaced by a later decision), or **open** (decided when its version is built).

## Purpose and scope

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| — | Build peons now. Claude Code's own tools don't cover: messages that survive a session exiting, peons in herdr panes, binding a session to a bead and worktree, and omp. v0 alone is thin; the real value is v1. Re-check Claude Code's native features before starting v1. | accepted | beads-995 notes |
| S1 | The spec lives in the repo: `docs/SPEC.md`, `GLOSSARY.md`, `docs/adr/`. | accepted | this file |
| S3 | Desk only; no phone path in v1 (that stays with beads-00m). Things that need the captain show up in `pns status`. | accepted | SPEC v1 outline |
| S4 | Claude Code only for v0/v1, behind a harness interface (`Launch`, `Ring`, `Alive`). omp comes in v1.1 through an in-process extension. | accepted | SPEC v1 outline |
| R5 | Linux only. | accepted | SPEC |
| R7 | Vocabulary: captain, chief, peon, session, logbook, entry, message, receipt, unread, inbox, doorbell, ring. | accepted | GLOSSARY |

## Messaging model

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| S2 | A doorbell carries only "you have unread messages", never a body. A send writes the entry first, then rings. A failed ring loses nothing. | accepted | ADR-0001 |
| R4 | Messages are addressed to a session; there is no publish/subscribe bus. Only a message wakes anyone. Topics, if ever needed, become a `subscriptions` table that turns them into addressed messages. | accepted | ADR-0001 |
| — | Claude Code's 50-message socket queue is not a limit for peons, because one doorbell covers all of a session's unread messages. | accepted | ADR-0001 |
| R1 | One append-only `entries` table. Unread = messages to me with no receipt from me, computed every time. No cursor, no time window, no mutable inbox. | accepted | ADR-0002 |
| R12 | A receipt's id is derived from the message and the session (`rcv:<msg>:<session>`) and written with `INSERT OR IGNORE`, so a duplicate is a no-op. | accepted | ADR-0002, SPEC |
| R13 | "Needs the captain" is a message addressed to `captain`, not a separate field. | open (v1) | SPEC v1 outline |

## Storage

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| R17 | The logbook is a SQLite file (`~/.local/share/peons/logbook.db`, WAL, `modernc.org/sqlite`). Writes use `BEGIN IMMEDIATE`. pns uses none of Dolt's features, and every session runs on one machine. | accepted | ADR-0005 |
| R15 | The record keeps the name "logbook" (not "ledger"); the table is `entries`. | accepted | GLOSSARY, SPEC |
| R2′ | One Dolt commit per write, with a disk-size warning instead of a GC timer. | superseded by R17 | ADR-0005 |
| R14 | Dolt's `GET_LOCK` instead of a flock file. | superseded by R17: SQLite's write lock | ADR-0003, ADR-0005 |
| — | Considered and rejected for the table: one table per kind (A), a read cursor (D), log plus a mutable inbox table (E), per-session Dolt branches as read markers (F). | rejected | ADR-0002 |
| — | Considered and rejected for storage: JSONL files (Claude Code's own pattern), which suit one writer per file but not a shared, queried log. | rejected | ADR-0005 |

## Sessions

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| R8a | Peons launch with `claude --name peon-<slug>` (lowercase letters, digits, `-`; never `:`). | accepted | SPEC |
| R8b | Only `pns` writes to session sockets; the chief doesn't use Claude's own cross-session send for peon traffic. | accepted | SPEC |
| R8c | Each peon worktree sets `"crossSessionInbound": "accept"`, because a session in bypass mode otherwise holds peer messages. | accepted | SPEC |
| R8d | The Claude adapter only talks to sessions with `peerProtocol == 1`, whose pid is alive with a matching start time. | accepted | SPEC |
| R16 | Whether a session is alive is worked out each time from Claude's registry, never stored. No `spawned`/`ended`/`reaped` entries and no reaping. | accepted | ADR-0004 |
| M4 | A session finds its own name from `--as`, then `PNS_SESSION`, then by walking up its parent processes to a Claude registry file. | accepted | SPEC |

## Waking and reconciling

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| R3 | From v1, all "what should happen now" logic is a pure `Plan(facts, now)`. It runs from send, hooks (5 s debounce), status, inbox and a 60 s watchdog. No daemon, no `pns tick`, no `pns wait`. One reconcile at a time. | accepted (v1) | ADR-0003 |
| R9 | One doorbell in flight per session; re-ring after 60 s; flag the chief after 120 s. | open (v1) | SPEC v1 outline |
| R10 | `pns send --now` rings with priority "now", which interrupts the current turn; the default is "next". | accepted | SPEC |
| R11 | Peon contract: a prompt starting with `[pns]` is a doorbell; run the `pns inbox` it names, act, and reply with `pns send`. | open (v1, with `pns spawn`) | SPEC v1 outline |

## Code and build

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| R6 | One binary, `pns`, from `cmd/pns`. Packages: `logbook`, `harness/claude`, `whoami` in v0; `plan`, `reconcile`, `hook`, `spawn`, `herdr` in v1. | accepted | SPEC |
| — | Dependency: `modernc.org/sqlite` (pure Go, no cgo; crew's choice too). | accepted | ADR-0005 |

## v0 scope (walking skeleton)

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| M1 | v0 is `pns init`, `send`, `inbox`, `status`; peons started by hand. | accepted | SPEC |
| M2 | A receipt means "shown by `pns inbox`"; `--peek` writes nothing; no hooks in v0. | accepted | SPEC |
| M3 | Ring every unread message on every send; no lock, no timers. A missed doorbell waits for the next send. | accepted | SPEC |
| M5 | The kind list is declared up front: `message`, `received`, `rang` (narrowed from M5's original list by R16). | accepted | SPEC |
| M6 | (Was: database on the shared beads server.) Now the SQLite file from R17. | superseded by R17 | ADR-0005 |
| M7 | Tests: logbook against a temp file; Claude adapter against a fake unix socket; a scripted end-to-end run with two real sessions. | accepted | SPEC |
| M8 | The doorbell explains itself; no peon prompt file in v0. | accepted | SPEC |
| M9 | Manual setup: `crossSessionInbound` in the worktree's settings, then `claude --name peon-a`. | accepted | SPEC |
