# Decisions

Every design decision for bees, from the design grill (bees-1, October 2026). Hard-to-reverse decisions also have an ADR in [adr/](adr/); behaviour is specified in [SPEC.md](SPEC.md); words are defined in [GLOSSARY.md](../GLOSSARY.md). Ids (S, R, M) match the decisions page used during the grill.

Status: **accepted**, **superseded** (replaced by a later decision), or **open** (decided when its version is built).

## Purpose and scope

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| — | Build bees now. Claude Code's own tools don't cover: messages that survive a session exiting, bees in herdr panes, binding a session to a bead and worktree, and omp. v0 alone is thin; the real value is v1. Re-check Claude Code's native features before starting v1. | accepted | bees-1 notes |
| S1 | The spec lives in the repo: `docs/SPEC.md`, `GLOSSARY.md`, `docs/adr/`. | accepted | this file |
| S3 | Desk only; no phone path in v1 (that stays with dotfiles-4). Things that need the keeper show up in `bs status`. | accepted | SPEC v1 outline |
| S4 | Claude Code only for v0/v1, behind a harness interface (`Launch`, `Ring`, `Alive`). omp comes in v1.1 through an in-process extension. | accepted | SPEC v1 outline |
| R5 | Linux only. | accepted | SPEC |
| R7 | Vocabulary: keeper, queen, bee, session, logbook, entry, message, receipt, unread, inbox, doorbell, ring. | accepted | GLOSSARY |

## Messaging model

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| S2 | A doorbell carries only "you have unread messages", never a body. A send writes the entry first, then rings. A failed ring loses nothing. | accepted | ADR-0001 |
| R4 | Messages are addressed to a session; there is no publish/subscribe bus. Only a message wakes anyone. Topics, if ever needed, become a `subscriptions` table that turns them into addressed messages. | accepted | ADR-0001 |
| — | Claude Code's 50-message socket queue is not a limit for bees, because one doorbell covers all of a session's unread messages. | accepted | ADR-0001 |
| R1 | One append-only `entries` table. Unread = messages to me with no receipt from me, computed every time. No cursor, no time window, no mutable inbox. | accepted | ADR-0002 |
| R12 | A receipt's id is derived from the message and the session (`rcv:<msg>:<session>`) and written with `INSERT OR IGNORE`, so a duplicate is a no-op. | accepted | ADR-0002, SPEC |
| R13 | "Needs the keeper" is a message addressed to `keeper`, not a separate field. | open (v1) | SPEC v1 outline |

## Storage

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| R17 | The logbook is a SQLite file (per project since R23, WAL, `modernc.org/sqlite`). Writes use `BEGIN IMMEDIATE`. bs uses none of Dolt's features, and every session runs on one machine. | accepted | ADR-0005 |
| R15 | The record keeps the name "logbook" (not "ledger"); the table is `entries`. | accepted | GLOSSARY, SPEC |
| R2′ | One Dolt commit per write, with a disk-size warning instead of a GC timer. | superseded by R17 | ADR-0005 |
| R14 | Dolt's `GET_LOCK` instead of a flock file. | superseded by R17: SQLite's write lock | ADR-0003, ADR-0005 |
| — | Considered and rejected for the table: one table per kind (A), a read cursor (D), log plus a mutable inbox table (E), per-session Dolt branches as read markers (F). | rejected | ADR-0002 |
| — | Considered and rejected for storage: JSONL files (Claude Code's own pattern), which suit one writer per file but not a shared, queried log. | rejected | ADR-0005 |

## Sessions

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| R8a | Bees launch with `claude --name bee-<slug>` (lowercase letters, digits, `-`; never `:`). | accepted | SPEC |
| R8b | Only `bs` writes to session sockets; the queen doesn't use Claude's own cross-session send for bee traffic. | accepted | SPEC |
| R8c | Each bee worktree sets `"crossSessionInbound": "accept"`, because a session in bypass mode otherwise holds peer messages. | accepted | SPEC |
| R8d | The Claude adapter only talks to sessions with `peerProtocol == 1`, whose pid is alive with a matching start time. | accepted | SPEC |
| R16 | Whether a session is alive is worked out each time (Claude's registry, herdr), never stored. No reaping. | accepted, amended by R18″ (`spawned`/`ended` entries record workspace ownership) | ADR-0004 |
| M4 | A session finds its own name from `--as`, then `BS_SESSION`, then by walking up its parent processes to a Claude registry file. | accepted | SPEC |

## Waking and reconciling

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| R3 | From v1, all "what should happen now" logic is a pure `Plan(facts, now)`. It runs from send, hooks (5 s debounce), status, inbox and a 60 s watchdog. No daemon, no `bs tick`, no `bs wait`. One reconcile at a time. | accepted (v1) | ADR-0003 |
| R9 | One doorbell in flight per session; re-ring after 60 s; flag the queen after 120 s. | open (v1) | SPEC v1 outline |
| R10 | `bs send --now` rings with priority "now", which interrupts the current turn; the default is "next". | accepted | SPEC |
| R11 | Bee contract: a prompt starting with `[bs]` is a doorbell; run the `bs inbox` it names, act, and reply with `bs send`. | open (v1, with `bs spawn`) | SPEC v1 outline |

## Code and build

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| R6 | One binary, `bs`, from `cmd/bs`. Packages: `logbook`, `project`, `bee`, `harness/claude`; `plan` and `reconcile` in v1. | accepted, packages revised by R24 | SPEC |
| — | Dependency: `modernc.org/sqlite` (pure Go, no cgo; crew's choice too). | accepted | ADR-0005 |

## v0 scope (walking skeleton)

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| M1 | v0 is `bs init`, `send`, `inbox`, `status`; bees started by hand. | accepted | SPEC |
| M2 | A receipt means "shown by `bs inbox`"; `--peek` writes nothing; no hooks in v0. | accepted | SPEC |
| M3 | Ring every unread message on every send; no lock, no timers. A missed doorbell waits for the next send. | accepted | SPEC |
| M5 | The kind list is declared up front: `message`, `received`, `rang`, `spawned`, `ended` (R16 narrowed it; R18″ restored `spawned`/`ended`). | accepted | SPEC |
| M6 | (Was: database on the shared beads server.) Now the SQLite file from R17. | superseded by R17 | ADR-0005 |
| M7 | Tests: logbook against a temp file; Claude adapter against a fake unix socket; a scripted end-to-end run with two real sessions. | accepted | SPEC |
| M8 | The doorbell explains itself; no bee prompt file in v0. | accepted | SPEC |
| M9 | Manual setup: `crossSessionInbound` in the worktree's settings, then `claude --name bee-a`. | accepted | SPEC |

## Spawning bees

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| R18″ | Spawn the crew way: `herdr worktree create` labelled with the bead, one `spawned` entry recording the herdr workspace id, `bs end` removes only that recorded id. No roster table; never guess a workspace from herdr. | accepted | ADR-0006 |
| R20 | One bee per bead in v1; a role prefix only when a second bee per bead is needed. | accepted | ADR-0006 |
| R21′ | One name everywhere: `bee-` + bead id without `beads-`, `.` → `-`. Session, herdr agent, branch, worktree dir (`<repo>-WT/<name>`) and address; herdr label is the bead id. Too long is refused, not hashed. | accepted | ADR-0006 |
| R23 | Project is the scope (crew's `--project`): `--project` → `BS_PROJECT` → git common-dir basename. Per-project logbook `~/.local/share/bees/<project>.db`, repo `$BS_REPOS/<project>`, queen `queen-<project>`. The bead's repo label must equal the project unless `--repo`. | accepted | ADR-0006 |
| R24 | Implementation: packages `project` and `bee`; typed `Herdr` and `Beads` ports with CLI adapters and stateful fakes; `spawned` written right after the worktree is created; first assignment delivered as Claude's first prompt; logbook gains `RecordSpawn`, `RecordEnd`, `LatestSpawn`. Spawn resumes a dead bee's recorded workspace and reports orphans. | accepted | ADR-0006 |
| R25 | Worktrees stay at `<repo>-WT/<name>`. Per-bee Claude settings ride `--settings` (Claude reads `settings.local.json` from the main checkout in a worktree); no `BS_*` variables; `bs end` deletes the branch only if fully merged. | accepted | ADR-0006 |
| R26 | Review fixes: the `spawned` entry and the brief are written in one transaction; the registry walk beats `BS_SESSION`, and `bs queen` sets no `BS_*`; the spawn records its repo so `bs end` deletes the branch there; the project label may be on the bead or any ancestor. | accepted | SPEC |
| R27 | Named project epics, as in crew: the epic id is the repo name (`bd create --id bees --force`), beads are `bees.<n>`. A bee's name is the bead id with `.` → `-` (`bees-8`); the project check is the `<project>.` prefix, replacing the label walk. Supersedes R21′'s `bee-` prefix and R23's label check. | accepted | ADR-0006 |
| R28 | Migrate bees now: epic `bees`; open bees-1 children renamed to `bees.<n>`; bees-1 stays as the design-spec epic. Other repos migrate when next worked on. | accepted | beads |
| R27⁵ | Jira-style ids: beads are `<project>-<n>[.<m>…]`, `bs epic` creates the next `<project>-<n>`. Project names are lowercased with `-` → `_` and must match `^[a-z][a-z0-9_]{0,24}$`, so names can't collide (tested: `web` vs `web-2`). A bee's name is the id with `.` → `-`; a bead belongs to the project if it matches `^<project>-\d+(\.\d+)*$`. Supersedes R27 (named epics; `bd` rejects a bare `bees` id) and R27′–R27⁗′. | superseded by R27⁶ | ADR-0006 |
| R27⁶ | Project names are one alphanumeric word: lowercased with `-` and `_` dropped (`pymatgen-analysis-defects` → `pymatgenanalysisdefects`), matching `^[a-z][a-z0-9]{0,24}$`. `bd query`'s id wildcard is a SQL LIKE, where `_` matches any character, so with no `_` `id="<project>-*"` is exact. Repos differing only in separators (`ab-c`, `a_bc`) share a name; set `BS_PROJECT` for one. Otherwise as R27⁵. No bead or logbook used `_`, so nothing migrates. | accepted | ADR-0006 |
| R28″ | Migration: bees-1 → `bees-1`, its children → `bees-1.<n>`. Supersedes R28. | accepted | beads |
| R29 | Resume a dead bee with `claude --continue` in its recorded worktree; herdr's `agent_not_ready` (Claude blocked at a startup prompt) counts as started; `bs spawn` warns when `~/.claude.json` shows the repo untrusted (read only). | accepted | SPEC |
| R30 | `bs status` lists sessions this project's logbook names that are running or have unread mail. Running sessions the logbook never named (other projects, sessions that haven't used bs) are left out, because Claude's registry is machine-wide; stopped sessions with nothing unread are history. Narrows bees-1.6's "every running Claude session". | accepted | SPEC |
| R31 | Renamed the project from peons to bees: repo and module `bees`, CLI `pns` → `bs` (`cmd/bs`), package `peon` → `bee`, peon → bee, chief → queen (`queen-<project>`, `bs queen`), `PNS_*` → `BS_*`, data in `~/.local/share/bees/`, beads `peons-*` → `bees-*`. Captain, logbook, doorbell/ring and spawn keep their names. Older commits, close reasons and logbook entries use the old words. | accepted | all docs |
| R32 | Beads stay inside the project through bs, not the prompt: `bs bead ls [--all] [<bd query>]` lists the project's beads (not closed by default; extra args ANDed), `bs bead new --parent <project>-N <title>` files one with `bd create --force` and refuses a missing or foreign parent, and `bs epic`'s hint points there. `bs bead ls` passes `bd query 'id="<project>-*" …'` through; it is exact because names have no `_` (R27⁶). The queen and every bee launch with `--append-system-prompt` set to `internal/prompt/system.md` (tracked in the repo, embedded in bs, `{{project}}` filled in), which names these commands and is where any other standing instruction for queens and bees goes; a bee in another project's repo (`--repo`, now or when first spawned) gets none, because its bs resolves that repo's project. No labels, no hook blocking raw `bd`. Glossary gains project, bead, epic. | accepted | SPEC, GLOSSARY |
| R39 | The launch path, after bees-1.14's dogfood failures. herdr refuses agent arguments with any Unicode control character: `bee.CheckAgentArgs` is that rule, applied by the adapter, used by fakeHerdr, and checked against the installed herdr by a side-effect-free contract test. Queen and workers get the system prompt as `--append-system-prompt-file ~/.cache/bees/<project>-system.md`. herdr's `timeout` is *unconfirmed*: Claude's registry (a new `Sessions` port) decides whether the worker started, and also whether a spawn would start a twin, failing closed; `Herdr.Agent` is gone; herdr waits 10 s, not 30. A restart uses `--continue` only if the worker read a message since this spawn. A brief refers to its spawn, and an ended spawn's unread brief is retired. `bs version`, plus a warning when bs runs in its own repo and was built from a commit behind `main`. Amends R24/R29. | accepted | ADR-0007, SPEC |
| R40 | Role prompts replace the shared `system.md` (bees-1.18): the queen launches with `internal/prompt/QUEEN.md`, workers with `WORKER.md`, each followed by `BEADS.md`, the bead rules written once; files `~/.cache/bees/<project>-queen.md` and `<project>-worker.md`. QUEEN: hand out beads with `bs spawn` (`--repo`), talk with `bs send`/`bs inbox`, read the fleet from `bs status` only, `bs end` to tear down, close a bead only after its PR is merged, file bs rough edges as beads. WORKER: own one bead in its worktree, read brief and `bd show`, read mail on a `[bs] … run: bs inbox` line, ask the queen when blocked, finish with a PR and `done: <bead> PR #N`, leave `bd close` to the queen. Fixes system.md blessing `bd close` for everyone. The first-prompt doorbell now reads `— run: bs inbox` like a ring. Settles R11 (the worker contract is WORKER.md). Amends R32. | accepted | SPEC |
| R41 | Herdr workspace labels are natural language (bees-1.20): `bs spawn --label "<words>"`, required on a first spawn, 1–4 lowercase words of `[a-z0-9]`, ≤ 32 characters, refused (never rewritten) when bad; the queen writes it from the bead (QUEEN.md gives the rules and examples). A restart reuses the recorded label and refuses another. The label is recorded in the spawn entry and shown by `bs status`. Identity never rides on it: an orphan is found by its worktree path `<repo>-WT/<name>`. Amends ADR-0006. | accepted | SPEC |
| R42 | `bs end` keeps the branch; `bs prune [--dry-run]` deletes branches (bees-1.17). `git branch -d` proves a merge by ancestry only, so after a rebase or squash merge `bs end` kept the branch and claimed unmerged work. Now `end` only removes the workspace and records `ended`, and prints `branch <b> kept; bs prune deletes it once merged`. `prune` looks only at branches of bees whose latest spawn has ended, as the logbook recorded them, and proves a merge by ancestry, a merged GitHub PR whose head is the branch tip, or every changed path identical on the default branch. It deletes with `git branch -D` and never touches remotes; kept branches say "not merged" (exit 3) or "could not check" (exit 4). Amends ADR-0006. | accepted | SPEC, ADR-0006 |
| R43 | `bs spawn` takes many beads (bees-1.35): `bs spawn --label L1 b1 --label L2 b2`, each `--label` going with the bead after it. Every bead is checked first (label rules, a first spawn needs a label, a bee named twice); any such refusal starts nothing and exits 2. Then each bee's worktree and spawn record are made one at a time, and the Claude starts (herdr waits ~10 s for each) run in parallel, so a fan-out costs one start, not one per bee. One line per bead; past the checks a failure doesn't stop the others, and any failure exits 1. `--repo` comes before the first bead and applies to all: one repo per call. | accepted | SPEC |
| R44 | The human's role is the keeper, not the captain (bees-1.46): it fits the hive (queen, bees; the keeper tends the hive and harvests the work). Renamed across GLOSSARY, docs, role prompts, skills and bakeoff cards and scripts (`keeper.log`, `keeper_turns`, `keeper-wip`). bakeoff/HISTORY.md rows and bakeoff/results/ keep "captain". Amends R7. | accepted | GLOSSARY |
| R19, R22, R18′ | Roster table, reserve-first spawn, label-only project. | superseded by R18″/R23 | ADR-0006 |

## Prior art for handoff (bees-1.1)

Ideas from desk↔phone and agent↔agent handoff tools (research 2026-10-10, prototype dotfiles-4), checked against what bees already decided. Nothing here adds an entry kind; the new fields ride in a message's `data`.

| Id | Decision | Status | Recorded in |
|---|---|---|---|
| R33 | Transport is Claude Code's cross-session socket, for doorbells only (already R8b, ADR-0001). Kept from the prior art: it delivers between tool calls when busy and starts a turn when idle, so bees need no Stop-hook queue; it never lets a peer answer a permission prompt, so a bee blocked on one needs the keeper, not the queen. No herdr-prompt or tmux send-keys injection: no receipt, and text/Enter races. The one exception is a spawned bee's first prompt, passed as a `herdr agent start` argument. | accepted | SPEC |
| R34 | Receipts are the acks (MCP Agent Mail's `acked_at`, already R1/R12): a message with no receipt is the unacked set, re-rung by R9. A receipt means "shown", not "done"; the answer is a reply message. | accepted | ADR-0002 |
| R35 | A message's `data` may carry `bead` (the thread: the bead id, as Agent Mail threads by task) and `needs`: `none` (default; report or FYI), `answer` (a question) or `approval` (a yes/no that blocks the sender). These are LangChain Agent Inbox's notify/question/review tiers, and A2A's `input-required` is `needs ≠ none`. No `kind`/`severity` envelope: who it's to and `needs` already say how loud it is. `bs send --needs answer\|approval`, `--bead` (default: the sender's own bead). | open (v1) | SPEC v1 outline |
| R36 | An answer is a message whose `ref` is the question's id (`bs send --re <id>`). A question is open while no message refers to it; the first answer wins and later ones are shown but change nothing (Claude Code Channels' "yes abcde" relay, without a second id: the entry id is the tag, and `bs inbox` prints its short suffix). `bs status` counts open questions per session alongside unread. | open (v1) | SPEC v1 outline |
| R37 | Escalation is addressing, not a push channel: `needs: approval` or `answer` to `keeper` (R13) is what `bs status` lists first. `none` to the keeper is a digest line, never listed first. No phone push and no presence gate (Remote Control's presence file), per S3. | open (v1) | SPEC v1 outline |
| R38 | A session's attention state comes from events, never from scraping its screen: Claude hooks (`bs hook`) and herdr's agent status (idle / working / blocked at a prompt) are facts for `Plan` (Kanban Code, herdr `pane.agent_status_changed`). A bee going idle with no open question and no reply since its last receipt can be flagged to the queen; bs never writes a "done" for it. | open (v1) | SPEC v1 outline |
| — | Considered and rejected: mirroring the whole session to the phone (Happy, Omnara), the opposite of a low-context queen; ACP or A2A as the transport (a server per session, too heavy on one machine); a presence-gated phone push (S3). | rejected | this file |
