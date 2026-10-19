# ![Beads. BEES? BEADS! BEADS?!?](docs/img/bees-beads.gif)

![beads → bees → bs](docs/img/beads-bees-bs.svg)

**One queen, many workers.** The **queen** is a taskmaster and never writes the code. It
splits your request into [beads](https://github.com/steveyegge/beads) (`bd`), spawns a
**worker** per bead in a labelled [herdr](https://herdr.dev) workspace, and reports back to
you. Each worker works on its own, in its own git worktree, and tells the queen when it is
done. You talk only to the queen. `bs` is the small Go CLI that ties Claude Code, herdr
and beads together.

![A design study, start to finish: the queen fans "how should the bee reach the flower?" out to three workers, the keeper picks zigzag in plannotator, that worker builds it with a steer mid-build, and a plain git merge lands it](docs/img/demo2.gif)

A design study from start to finish: three workers prototype in parallel, the keeper picks
one in plannotator, the worker that already has the context builds it, and a plain
`git merge` lands it. Replay it with [demo/demo2/run.sh](demo/demo2/RUN.md).

```mermaid
flowchart LR
    K[keeper] -- types --> Q["queen<br/>(Claude Code)"]
    Q -- "1 bs send" --> L[("logbook")]
    Q == "2 ring: Claude Code's<br/>messaging socket" ==> W["worker<br/>(Claude Code)"]
    W -- "3 bs inbox" --> L
    S[["per-worker --settings"]] -. permissions .-> W
    H[herdr] -. "starts it; holds<br/>pane + worktree" .-> W
```

bs never types into a terminal to move the conversation along. A message is stored in the
logbook first. Then bs rings the recipient over Claude Code's own cross-session messaging
socket, found through `~/.claude/sessions`, and the ring arrives as a prompt in that
session's conversation. Each worker gets its permissions from its own `--settings` file;
herdr only starts the sessions and holds their panes and worktrees, and it carries no
messages.

![The keeper asks the queen for three jobs in a Claude Code chat; the queen calls bs epic and bs bead new, bs spawn opens three labelled herdr workspaces, the workers report done in bs inbox, and after "merge all three" the queen calls bd close, bs end and bs prune](docs/img/queen.gif)

**Every piece of work is a bead.** The bead is the worker's brief, its name (`bees-2.1` →
session, branch and worktree `bees-2-1`) and its exit: the work is done only when its PR
merges and the queen runs `bd close`. Nothing runs that isn't tracked.

![The worker's first prompt is a doorbell; bs inbox shows its brief from the bead, it does the work, pushes, and reports with bs send queen-t2c "done: …"](docs/img/worker.gif)

Queen and workers talk with `bs send` and `bs inbox`. Each message lands in a SQLite
**logbook** first; then a **doorbell** prompt goes into the recipient's Claude session,
so an idle session acts on it without anyone typing.

The two chat GIFs replay bake-off run t2c ([bakeoff/cards/S2.md](bakeoff/cards/S2.md),
a toy repo that pushes branches instead of opening PRs). The herdr window and the Claude
Code chat are an illustration; the keeper's lines, the tool calls, their output and the
sessions' replies are copied from the run's transcripts (`…` marks a cut). Rebuild them
with `uv run docs/img/src/gifs.py`.

## Install

Linux or macOS. Needs [Go](https://go.dev/dl/), [Claude Code](https://code.claude.com)
2.1.296 or later (cross-session messaging), [herdr](https://herdr.dev) to run workers,
and [`bd`](https://github.com/steveyegge/beads) for beads.

```sh
git clone https://github.com/jmmshn/bees.git && cd bees
go install ./cmd/bs
bs version
```

Then start `herdr`, `cd` into your project, give it a beads store (`bd init`, or one shared
store via `BEADS_DIR`), and run `bs queen`.

Protect your project's `main` on GitHub (require a pull request). Workers push their own
branches in auto mode; bs's deny rules refuse the obvious pushes to `main`, but branch
protection is the real guard ([docs/SETUP.md](docs/SETUP.md#worker-permissions)).

Full setup: [docs/SETUP.md](docs/SETUP.md). For a guided setup, open Claude Code in this
repo and run the [`/setup-bees`](.claude/skills/setup-bees/SKILL.md) skill; it installs
and checks each piece in turn.

Details: [docs/SPEC.md](docs/SPEC.md) · decisions: [docs/adr/](docs/adr/) ·
vocabulary: [GLOSSARY.md](GLOSSARY.md)

---

## Trade-offs

Unlike subagents, a worker is a full Claude Code session that outlives the queen that
spawned it and wakes when a message arrives.

The price: Linux or macOS, Claude Code sessions only, one machine. The doorbell rides on
Claude's own cross-session socket, which keeps nothing after a session exits and drops
identical repeats ([ADR-0001](docs/adr/0001-logbook-is-the-only-truth.md)).
So bs uses it only to say "you have messages" and keeps every message in the logbook:
a lost ring only delays a message, and a doubled one can't deliver it twice.

---

## Commands

```text
bs queen                      start claude --name queen-<project> in the repo
bs epic <title>               create the project's next epic, <project>-<n>
bs bead ls | new …            list the project's beads, or file one under an epic
bs spawn --label L <bead> …   start a worker per bead, in parallel, each in its own herdr workspace
bs send [--now] <to> <body|-> store a message, then ring <to>
bs inbox [--peek]             print unread; without --peek, record receipts
bs status [--json]            sessions, running or not, and unread counts
bs end [--force] <worker|bead> remove its recorded workspace, keep its branch; --force discards uncommitted files
bs prune [--dry-run]          delete ended workers' local branches once merged
bs version                    print the commit bs was built from
bs init                       create the logbook (bs queen and spawn do too; others refuse without one)
```

Every command takes `--project`; `send`, `inbox`, `spawn` and `end` take `--as <session>`
for use outside a named Claude session; `spawn --repo <path>` puts workers in another repo.

---

## How it works

### Components

```mermaid
flowchart LR
    subgraph keeper["Keeper (you)"]
        H[talks only to the queen]
    end
    subgraph queen["claude --name queen-bees"]
        C[bs spawn / send / inbox]
    end
    subgraph workers["workers, one per bead"]
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

### A round trip, queen → worker → queen

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

`scripts/e2e.sh` checks this with real sessions: the idle worker's doorbell lands about
20 ms after `bs send`, and a full queen → worker → queen round trip needs nobody typing.

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

### Spawning a worker

```mermaid
flowchart LR
    S["bs spawn bees-2.1"] --> N["name = bees-2-1<br/>(one string: session, herdr agent,<br/>branch, worktree dir, address)"]
    N --> X{open 'spawned'<br/>entry?}
    X -- "yes, session live<br/>(Claude's registry)" --> E1[ErrRunning]
    X -- "yes, session gone" --> RS["restart in the same workspace<br/>claude --continue if it read its inbox"]
    RS --> ST
    X -- no --> WT["herdr worktree create<br/>bees-WT/bees-2-1"]
    WT --> REC["RecordSpawn + brief (bd show)<br/>in one transaction"]
    REC --> ST["herdr agent start … claude --name bees-2-1<br/>--settings per-worker JSON<br/>first prompt = doorbell"]
    ST -- "herdr timeout" --> C{"session in<br/>Claude's registry?"}
    C -- yes --> OK[started]
    C -- no --> E2["error: read its pane"]
```

`bs end bees-2-1` removes only the workspace recorded in that `spawned` entry and writes
`ended`; it keeps the branch. `bs prune` later deletes ended workers' local branches once
their work is on main, including after a rebase or squash merge.

### The logbook is the only truth

```text
entries(id, ts, kind, actor, subject, ref, data)      -- insert-only, STRICT
kind ∈ message | received | spawned | ended            (rang: v1)

unread(s) = messages to s with no 'received' entry from s, in insertion order
```

Nothing records whether a session is alive
([ADR-0004](docs/adr/0004-liveness-is-derived-not-stored.md)); that is worked out each
time from Claude's registry and `/proc`. A dead session needs no cleanup: its messages wait.

## Status

v0 on Linux: every "Done when" item in [SPEC](docs/SPEC.md#done-when) passes (`go test ./...`
and `scripts/e2e.sh <throwaway bead>`). The [v1 outline](docs/SPEC.md#v1-outline) adds
re-ringing, hooks, a keeper inbox and an adapter for [omp](GLOSSARY.md#outside-names).

## License

[MIT](LICENSE).
