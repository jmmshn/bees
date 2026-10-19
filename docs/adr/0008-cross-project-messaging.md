# Cross-project messages go to the recipient's home logbook (draft)

Status: **proposed** (bees-8.9). Nothing here is built; the keeper decides.

Today bs keeps every session inside one project. bees-8.7 (PR #49) made that strict: an inherited `BS_PROJECT` is refused in another project's repo. The keeper wants workers to talk to other projects' queens. The use cases are:

- **Ask for a change.** A worker in project A needs an API change in project B and asks `queen-b` for it.
- **Answer.** `queen-b` (or B's worker) replies, or says "filed b-12, will tell you when it merges".
- **Cross-project epics.** A's work waits on B's bead and goes on once it closes.

## What happens today

A worker can already reach another queen, but only one way:

```text
bees-8-9 ▸ bs send --project b queen-b "need Foo.Bar exported"
           → b.db, from bees-8-9, rings queen-b              (works: queen-b is b's queen)
queen-b  ▸ bs send bees-8-9 "done in b-12"
           → b.db; warning: bees-8-9 is not queen-b or a worker spawned in b,
             so bs stored the message without ringing it      ← reply never seen
bees-8-9 ▸ bs inbox                                           (reads bees.db) → no unread messages
```

The question goes into B's logbook, and so does the reply. The worker's home logbook is `bees.db`, so it never sees the reply unless it polls `bs inbox --project b`. Picking a project with `--project` is also the kind of ambient choice bees-8.7 was about (motif 14). A wrong `--project` writes into another project's logbook without any warning.

## Decision

**A session's name says which project it belongs to, so a message is stored in the recipient's home logbook.** Names are already unique across the machine and carry their project (ADR-0006, R27⁶). Project names have no `-`, so the mapping from name to project is a pure forward parse, not a reversed lossy name (motif 3):

```go
// Home returns the project a session belongs to, or "" for a name that carries none.
func Home(name string) string
// queen-<p>               → p
// <p>-<n>(-<m>)*          → p   (a worker: its bead id with . → -)
// keeper, BS_SESSION=free → ""  (falls back to the resolved project, as today)
```

Order matters for a project named `queen`. Its workers are `queen-3` and `queen-3-1`, which look like `queen-<p>`. Try the worker pattern first: `queen-3` then gives `queen`, because a project name must start with a letter (R27⁶), so `3` can't be a project. That project's own queen is `queen-queen`, which still gives `queen`. The parse is unambiguous as long as the worker pattern comes first, and a test should pin `queen-3`, `queen-3-1` and `queen-queen`.

`bs send <to>` writes to `Home(to)`'s logbook, from the sender's registry name. It rings `to` only if `to` is that project's queen or a worker recorded there. This is the bees-7.4 rule, "bs rings only its own project's sessions", checked against the recipient's logbook instead of the sender's. With this rule:

- **Replies come back on their own.** `queen-b ▸ bs send bees-8-9` lands in `bees.db` and rings, so the worker's plain `bs inbox` shows it.
- **Messaging needs no `--project`.** The address is the routing. `--project` and `BS_PROJECT` stay for what really has a project scope: beads, spawn, status and inbox. A mistyped address writes to a logbook nobody reads, and that case still gets the existing stranger warning. It no longer depends on which repo you `cd`'d into.
- **One write, one logbook.** The entry is the only truth (ADR-0001). There is no mirror copy in the sender's logbook, so no two-database write can half-fail (motif 8).

**Beads never cross projects.** A worker doesn't file a bead in B, and `bs bead new` keeps refusing a foreign parent (R32). Instead it asks `queen-b`, who files the bead in B and replies with its id. On the A side a **proxy bead** waits on it. The keeper's tests in the bead notes show `bd ready` ignores deps across id prefixes, so the dependency has to be a same-project one:

```text
bs bead wait <a-bead> --for b-12
  → files a-N "waits on b-12" with status blocked; a-bead depends on a-N
  → refuses b-12 if it doesn't exist (bd accepts it silently)
bs status / bs bead sync
  → closes a-N once every --for target is closed, which unblocks a-bead
```

A cross-project epic is then two epics, one per project, joined by proxies. There is no shared epic.

**Prompt rules** (WORKER.md and QUEEN.md, true in any project):

- A worker that asks another project tells its own queen and lists the ask in its done message. The truth stays in B's logbook, and A's queen learns of it through mail.
- A queen treats mail from another project's session as a request, not an order. She files a bead or declines, and she asks her keeper when the request is out of scope.

## Identity and authentication

Sending as someone else gets worse with this change: a spoofed `--as` now reaches every project's logbook, not just one. **bees-8.8 (the orphaned `bs` that skips the process-tree check) must be fixed before this ships.**

bees-8.8 is building an env fallback: when the parent walk finds no Claude ancestor, bs takes `CLAUDE_PID` and `CLAUDE_CODE_SESSION_ID` from the environment and checks them against `~/.claude/sessions`. That fits cross-project routing as it is. The sender's name comes from Claude's machine-wide registry, never from a logbook, so `bs send` resolves `from` the same way whichever logbook it writes to. The recipient's logbook doesn't need to check anything against the sender's home logbook, and no secret has to be shared between projects.

What this needs from bees-8.8:

- **Project-independent identity.** `Me()` must not consult any logbook or `BS_PROJECT`, so the identity is the same in every logbook. The env fallback already works this way.
- **Fail closed.** A registry entry that is missing or doesn't match refuses `--as` instead of trusting it. This matters more here, because a failure now reaches every logbook.

The limit stays the one bees-8.8 states. Any process running as the same user can set those variables, or write the logbook files directly. bees has no boundary against the user's own processes. Its boundaries are against the wrong store by accident (bees-8.7) and against a session claiming another session's name.

The boundary this change has to keep is attention. A ring is a user turn, so bs rings only sessions recorded in the target's own logbook.

## Considered options

- **Keep `--project` as the cross-project switch and add a reply path.** Rejected: the sender still picks a store by hand, which is bees-8.7's motif. The reply path would also need the recipient to know where the sender lives, and the name already says that.
- **One machine-wide logbook.** Rejected: it undoes the per-project logbook (R17/R23). `bs status`, unread counts and ownership of teardown are all scoped to the project.
- **Mirror each cross-project message into both logbooks.** Rejected: a write to two databases that isn't atomic, and two copies that can disagree.
- **Workers file beads in other projects.** Rejected: this is the blast radius bees-8.7 closed. Another project's beads are its queen's to file.
- **bd's own cross-project deps (`bd ship` + `external:<project>:<capability>`).** This is the closest prior art for the proxy. It resolves through `external_projects` config, which points at separate stores. Whether it works in bees' one shared store is untested, so it needs a probe before `bs bead wait` is built. If it works, it replaces the sweep.

## Prior art

- **Email addresses.** The domain in `user@domain` routes the mail, and a reply goes to the sender's own domain. `Home(name)` works the same way.
- **MCP Agent Mail.** Projects are isolated, and messaging across projects needs an explicit contact handshake (contact policies). bees gets the same consent through the queen prompt rules and the ring rule, without a handshake table. Add one if open messaging turns out noisy.
- **bd `ship` / `external:` deps.** See above.
- **crew** (the private predecessor) scoped everything by `--project` with no messages across projects. That is the scope bees inherited in ADR-0006.

## Recommendation

Build it in this order, each step as its own bead:

1. Land bees-8.8 (env fallback, fail closed, `Me()` independent of the project). It blocks the rest.
2. Add `Home(name)` and route `bs send` by it, checking the ring rule against the recipient's logbook. Replay `bs send` / `bs inbox` live between two throwaway projects.
3. Add the prompt rules for cross-project asks (bakeoff replay, since this touches messaging).
4. Probe `bd ship`/`external:` in the shared store. Then build `bs bead wait` on top of it, or as the proxy sweep if it doesn't work.

Until then, the refusal from bees-8.7 stays.

## Consequences

- `bs send` opens a second logbook when the recipient is in another project. `bs inbox`, `bs status` and the bead commands are unchanged.
- The stranger warning (bees-1.54) only fires for names whose `Home` has no such session, or for names with no home at all.
- A `--repo` worker is named after its spawner's bead, so its `Home` is the spawner's project. That already matches R46.
