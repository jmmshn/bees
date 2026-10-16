# bees

> `bs` is a small Go CLI that runs one Claude Code session per bead on a codebase,
> and lets those sessions hand each other work and **wake each other up**, with nobody typing.

**Every piece of work is a bead.** A **queen** session files work as
[beads](https://github.com/steveyegge/beads) (`bd`) and spawns a **bee** per bead: a
Claude Code session in its own git worktree. The bead is the bee's brief, its name
(`bees-2.1` → session, branch and worktree `bees-2-1`) and its exit: the work is done
only when its PR merges and the queen runs `bd close`. Nothing runs that isn't tracked.

Queen and bees talk with `bs send` and `bs inbox`. Each message lands in a SQLite
**logbook** first; then a **doorbell** prompt goes into the recipient's Claude session,
so an idle session acts on it without anyone typing.

## Install

Private repo, Linux only. Needs [Go](https://go.dev/dl/), [Claude Code](https://code.claude.com)
2.1.296 or later (cross-session messaging), [herdr](https://herdr.dev) to run bees,
and [`bd`](https://github.com/steveyegge/beads) for beads.

```sh
git clone git@github.com:jmmshn/bees.git && cd bees
go install ./cmd/bs
bs version
```

Then start `herdr`, `cd` into your project, give it a beads store (`bd init`, or one shared
store via `BEADS_DIR`), and run `bs queen`.

Full setup: [docs/SETUP.md](docs/SETUP.md). For a guided setup, open Claude Code in this
repo and run the [`/setup-bees`](.claude/skills/setup-bees/SKILL.md) skill; it installs
and checks each piece in turn.

![Lindsay: "Beads." Gob: "BEES?" Lindsay: "BEADS!" Gob: "BEADS?!?"](docs/img/bees-beads.gif)

![beads (bd, the tracker), misheard as bees (the fleet), shortened to bs (the CLI)](docs/img/beads-bees-bs.svg)

Details: [docs/SPEC.md](docs/SPEC.md) · decisions: [docs/adr/](docs/adr/) ·
vocabulary: [GLOSSARY.md](GLOSSARY.md)

---

## How bs compares

The usual ways to put several agents on one job, and what each gives you:

- **Subagent fan-out** (Claude Code's Task tool, orchestrator–worker): the parent starts
  workers inside its own session; each returns one final result and ends with its task.
- **Agent-graph frameworks** (LangGraph, CrewAI, AutoGen): you code the agents and the
  edges between them; one program runs the graph and hands each agent its turn.
- **Shared queue or blackboard**: agents post to and poll a common store.
- **Human as the bus**: separate chat sessions; you copy the brief across and the answer back.
- **tmux + send-keys**: separate sessions in panes; a script types into the other pane.

| | Subagents | Agent graph | Queue / blackboard | Human as bus | tmux send-keys | **bs** |
|---|---|---|---|---|---|---|
| Each agent is a full Claude Code session in its own worktree | no, a task inside the parent (worktree optional) | no, agents you code | depends on the agents | yes, if you make one | yes, if you make one | **yes**, `bs spawn` |
| Agents outlive their lead | no | resumable from a checkpointer | yes | yes | yes | **yes** |
| An agent messages its lead mid-task | final result only; the lead can message it | when the graph routes to it | yes | you relay | keystrokes | **yes**, `bs send` |
| An idle agent acts on a message | n/a | n/a | only if it polls | you type | if the keys land | **yes**, doorbell |
| Message kept while the recipient is down | n/a | in the checkpoint | yes | only if you saved it | no | **yes**, logbook |
| Record of who said what | parent transcript | framework traces | the store | scrollback | scrollback | **yes**, append-only |
| Work tied to a tracked issue | no | no | no | by discipline | by discipline | **yes**, one bead each |
| Watch or steer an agent live | watch only | pause and edit state at interrupts | post to the store | yes | yes | **yes**, the bee's herdr pane |

What bs costs you: Linux, Claude Code sessions only, one machine. The doorbell rides on
Claude's own cross-session socket, which keeps nothing after a session exits and drops
identical repeats ([ADR-0001](docs/adr/0001-logbook-is-the-only-truth.md)).
So bs uses it only to say "you have mail" and keeps every message in the logbook: a lost
ring only delays a message, and a doubled one can't deliver it twice.

### Without bs: you are the message bus

![Two plain Claude Code sessions: the human copies the brief across and copies the answer back](docs/img/without-bs.svg)

### With bs: the sessions ring each other

![Queen spawns a bee, sends it a note; the bee is woken by a doorbell, works, replies; the queen is woken in turn](docs/img/with-bs.svg)

### Messages survive a session that is down

![bs send to a stopped session prints "queued"; the messages show up in bs inbox when it restarts](docs/img/queued.svg)

The first two images illustrate the Claude Code UI. Every `bs` line in them, and the
whole third image, is real v0 output.

---

## Commands

```text
bs queen                      start claude --name queen-<project> in the repo
bs epic <title>               create the project's next epic, <project>-<n>
bs bead ls | new …            list the project's beads, or file one under an epic
bs spawn --label L <bead> …   start a bee per bead, in parallel, each in its own herdr workspace
bs send [--now] <to> <body|-> store a message, then ring <to>
bs inbox [--peek]             print unread; without --peek, record receipts
bs status [--json]            sessions, running or not, and unread counts
bs end <bee>                  remove the bee's recorded workspace; keep its branch
bs prune [--dry-run]          delete ended bees' local branches once merged
bs version                    print the commit bs was built from
bs init                       create the logbook (optional; any command does)
```

Every command takes `--project`; `send`, `inbox`, `spawn` and `end` take `--as <session>`
for use outside a named Claude session; `spawn --repo <path>` puts bees in another repo.

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
    LB[("logbook<br/>~/.local/share/bees/&lt;project&gt;.db<br/>SQLite WAL, append-only")]
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
`ended`; it keeps the branch. `bs prune` later deletes ended bees' local branches once
their work is on main, including after a rebase or squash merge.

### The logbook is the only truth

```text
entries(id, ts, kind, actor, subject, ref, data)      -- insert-only, STRICT
kind ∈ message | received | spawned | ended            (rang: v1)

unread(s) = messages to s with no 'received' entry from s, in insertion order
```

Nothing records whether a session is alive
([ADR-0004](docs/adr/0004-liveness-is-derived-not-stored.md)); that is worked out each
time from Claude's registry and `/proc`. A dead session needs no cleanup: its mail waits.

## Status

v0 on Linux: every "Done when" item in [SPEC](docs/SPEC.md#done-when) passes (`go test ./...`
and `scripts/e2e.sh <throwaway bead>`). The [v1 outline](docs/SPEC.md#v1-outline) adds
re-ringing, hooks, a captain inbox and an omp adapter.
