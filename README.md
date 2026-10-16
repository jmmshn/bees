# bees

> Private. A small Go CLI, `bs`, that lets several Claude Code sessions on one codebase
> hand each other work and **wake each other up**, with nobody typing in either window.

A **queen** session splits work into beads and spawns a **bee** for each one, in its own
git worktree. They talk through `bs send` and `bs inbox`. Each message is stored in a
SQLite **logbook** first, and then a **doorbell** prompt is pushed into the recipient's
Claude session so it acts even when it was sitting idle.

```sh
go build -o bin/bs ./cmd/bs
go test ./...
```

![Lindsay: "Beads." Gob: "BEES?" Lindsay: "BEADS!" Gob: "BEADS?!?"](docs/img/bees-beads.gif)

> **Lindsay:** Beads. **Gob:** BEES? **Lindsay:** BEADS. **Gob:** BEADS?
> **Michael:** Gob's not on board.
>
> *Arrested Development*, S1E22 "Let 'Em Eat Cake". The work is tracked in beads (`bd`).
> GIF via [Tenor](https://tenor.com/search/arrested-development-bees-gifs).

Setup: [docs/SETUP.md](docs/SETUP.md) · details: [docs/SPEC.md](docs/SPEC.md) ·
decisions: [docs/adr/](docs/adr/) · vocabulary: [GLOSSARY.md](GLOSSARY.md)

---

## Why not just Claude Code?

Claude Code is good at running *one* agent on a task, and **subagents** fan out *inside* that
session and report back to it. Neither lets two long-running sessions, each with its own
context and conversation, coordinate without a human relaying between them.

### Without bs: you are the message bus

![Two plain Claude Code sessions: the human copies the brief across and copies the answer back](docs/img/without-bs.svg)

### With bs: the sessions ring each other

![Queen spawns a bee, sends it a note; the bee is woken by a doorbell, works, replies; the queen is woken in turn](docs/img/with-bs.svg)

### Messages survive a session that is down

![bs send to a stopped session prints "queued"; the messages show up in bs inbox when it restarts](docs/img/queued.svg)

The first two images are illustrations of the Claude Code UI. Every `bs` line in them,
and the whole third image, is real v0 output.

### What each option gives you

| Need | Plain sessions | Subagents (Task tool) | Claude's peer socket alone | **bs** |
|---|---|---|---|---|
| Own worktree and long-lived conversation per worker | yes | worktree optional; ends with its task | yes | **yes**, `bs spawn` |
| Worker outlives the parent session | yes | no | yes | **yes** |
| Worker → coordinator, unprompted | human relays | only its final result | yes, if you find the socket | **yes** |
| Wake an idle session | human types | n/a | yes | **yes**, doorbell |
| Message kept if the recipient is down | no | n/a | no, nothing kept after exit | **yes**, logbook |
| No silent drops | n/a | n/a | no: at most 50 queued, identical repeats dropped | **yes** |
| Audit trail of who said what | scrollback | parent transcript | none | **yes**, append-only table |
| Address by name, not pid and socket | no | n/a | no | **yes** |

The socket limits in the fourth column come from
[ADR-0001](docs/adr/0001-logbook-is-the-only-truth.md). bs uses that socket only to say
"you have mail" and keeps every message body in the logbook, so a lost or duplicated ring
can never lose a message.

---

## How it works

### Components

```mermaid
flowchart LR
    subgraph captain["Captain (you)"]
        H[talks only to the queen]
    end
    subgraph queen["claude --name queen-bees"]
        C[bs spawn / send / inbox]
    end
    subgraph bees["bees, one per bead"]
        P1["bees-2-1<br/>worktree bees-WT/bees-2-1"]
        P2["bees-2-2<br/>worktree bees-WT/bees-2-2"]
    end
    LB[("logbook<br/>~/.local/share/bees/bees.db<br/>SQLite WAL, append-only")]
    REG[["~/.claude/sessions/&lt;pid&gt;.json<br/>Claude's session registry"]]
    HD[herdr<br/>worktrees + panes]
    BD[bd<br/>beads]

    H --> C
    C -- "1 store" --> LB
    P1 -- "1 store" --> LB
    P2 -- "1 store" --> LB
    C -. "2 find by name" .-> REG
    C == "3 ring (unix socket)" ==> P1
    P1 == "ring" ==> C
    C -- "spawn" --> HD
    C -- "brief from" --> BD
    HD -- "starts" --> P1
    HD -- "starts" --> P2
```

### A round trip, queen → bee → queen

```mermaid
sequenceDiagram
    autonumber
    participant C as queen-bees
    participant L as logbook (SQLite)
    participant R as ~/.claude/sessions
    participant P as bees-2-1 (idle)

    C->>L: bs send bees-2-1 "…"  (INSERT message)
    C->>R: Find("bees-2-1")
    R-->>C: pid, procStart, socket, token
    Note over C,R: kept only if peerProtocol = 1,<br/>/proc/pid exists and its start time = procStart
    C->>L: Unread("bees-2-1")
    C->>P: NDJSON: auth line (if token), then user line, then half-close
    Note over P: arrives as a prompt:<br/>[bs] 1 unread (queen-bees) — run: bs inbox
    P->>L: bs inbox  (read, record receipts)
    P->>P: does the work
    P->>L: bs send queen-bees "pong: …"
    P->>C: ring
    C->>L: bs inbox
```

`scripts/e2e.sh` checks this with real sessions: the idle bee's doorbell lands about
20 ms after `bs send`, and a full queen → bee → queen round trip needs nobody typing.

### What one `bs send` does

```mermaid
flowchart TD
    A["bs send &lt;to&gt; body"] --> B["INSERT message entry<br/>(BEGIN IMMEDIATE)"]
    B --> O["print: sent &lt;id&gt; to &lt;to&gt;"]
    O --> F{"claude.Find(to)"}
    F -- "no live session<br/>by that name" --> Q["print: queued; &lt;to&gt; is not running<br/>exit 0"]
    F -- found --> U["Unread(to) → doorbell text"]
    U --> RG{"Ring(priority:<br/>--now ? now : next)"}
    RG -- ok --> D[exit 0]
    RG -- error --> W["stderr: couldn't ring &lt;to&gt;<br/>(the message is stored)<br/>exit 0"]
    Q -. "read when it next starts:<br/>its first prompt is a doorbell" .-> F
```

The message is stored before anything can fail, so the worst case is a late message,
never a lost one.

### Spawning a bee

```mermaid
flowchart LR
    S["bs spawn bees-2.1"] --> N["name = bees-2-1<br/>(one string: session, herdr agent,<br/>branch, worktree dir, address)"]
    N --> X{open 'spawned'<br/>entry?}
    X -- "yes, session live<br/>(Claude's registry)" --> E1[ErrRunning]
    X -- "yes, session gone" --> RS["restart in the same workspace<br/>claude --continue if it read its inbox"]
    RS --> ST
    X -- no --> WT["herdr worktree create<br/>bees-WT/bees-2-1"]
    WT --> REC["RecordSpawn + brief (bd show)<br/>in one transaction"]
    REC --> ST["herdr agent start … claude --name bees-2-1<br/>--settings crossSessionInbound=accept<br/>first prompt = doorbell"]
    ST -- "herdr timeout" --> C{"session in<br/>Claude's registry?"}
    C -- yes --> OK[started]
    C -- no --> E2["error: read its pane"]
```

`bs end bees-2-1` removes only the workspace recorded in that `spawned` entry and writes
`ended`; it keeps the branch. `bs prune` later deletes ended bees' local branches once their
work is on main, including after a rebase or squash merge.

### The logbook is the only truth

```text
entries(id, ts, kind, actor, subject, ref, data)      -- insert-only, STRICT
kind ∈ message | received | spawned | ended            (rang: v1)

unread(s) = messages to s with no 'received' entry from s, in insertion order
```

Nothing records whether a session is alive
([ADR-0004](docs/adr/0004-liveness-is-derived-not-stored.md)). That is worked out each
time from Claude's registry and `/proc`. A dead session needs no cleanup: its mail waits.

---

## Commands

```text
bs queen                      start claude --name queen-<project> in the repo
bs epic <title>               create the project's next epic, <project>-<n>
bs bead ls | new …            list the project's beads, or file one under an epic
bs spawn --label L <bead> …   start a bee per bead in parallel, each in its own herdr workspace, labelled L
bs send <to> [--now] <body|-> store a message, then ring <to>
bs inbox [--peek]             print unread; without --peek, record receipts
bs end <bee>                 remove the bee's recorded workspace; keep its branch
bs prune [--dry-run]          delete ended bees' local branches once merged
bs status [--json]            running sessions and unread counts
bs init                       create the logbook (optional)
```

To run a bee by hand, without herdr:

```sh
claude --name bee-a --settings '{"crossSessionInbound":"accept"}'
```

Without `crossSessionInbound: accept`, a session in bypass mode holds peer messages
instead of acting on them.

## Status

v0 on Linux: every "Done when" item in [SPEC](docs/SPEC.md#done-when) passes (`go test ./...`
and `scripts/e2e.sh <throwaway bead>`). The [v1 outline](docs/SPEC.md#v1-outline) adds
re-ringing, hooks, a captain inbox and an omp adapter.
