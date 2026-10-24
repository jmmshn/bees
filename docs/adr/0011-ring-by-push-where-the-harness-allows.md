# Ring by push where the harness allows it, a waiter where it doesn't

> Amends [ADR-0009](0009-core-and-adapters.md) (omp's ring: the extension runs the waiter, no socket or token per worker; agy's ring: the waiter, not "none until proven") and [ADR-0010](0010-core-rebuild.md)'s `Ring`. Decided by the keeper, 2026-10-23 (bees-13.91), after #168 built agy's waiter.

A doorbell only changes *when* a session reads its mail, never *whether* (S2, motif 10): `bs send` writes the message to the logbook first, and the worker reads it with `bs inbox`. So the ring can differ per harness without risking a message. It has to, because the harnesses don't offer the same things:

| harness | push from outside into an idle session | a finished in-session waiter wakes it |
|---|---|---|
| Claude Code | ✅ cross-session message (today's ring) | ✅ a finished background Bash starts a turn |
| agy 1.4.6 | ❌ see [Why agy can't take a push](#why-agy-cant-take-a-push-today) | ✅ only when a background command exits |
| pi / omp | ✅ the bs extension's `pi.sendMessage(…, {triggerTurn: true, deliverAs: "followUp"})` starts a turn when idle and queues one when busy | ✅ the same extension can run the waiter in-process |

**Decision: push where the harness allows it, a waiter where it doesn't.** The core stays one: the logbook, the doorbell text, `bs inbox --wait` and the `bs end` gate. Only each adapter's `Ring` differs.

```text
                ┌──────────── logbook (truth) ────────────┐
 bs send ─store─┤                                         │
                └──────────────────▲──────────────────────┘
                                   │ bs inbox --wait (polls 2 s, one per worker)
     Claude Code            pi / omp                     agy
     Ring = push:           Ring = push:                 Ring = the waiter (#168):
     cross-session msg      the bs extension runs        hooks ask the model to run
     to the recorded pid    bs inbox --wait in-process,  bs inbox --wait in the
                            then pi.sendMessage(         background; its exit
                            triggerTurn, followUp)       starts agy's next turn
```

- **Claude Code: push**, today's cross-session message to the recorded pid (R33, ADR-0009). It stays a push; it does not move to a waiter.
- **pi/omp (bees-12.8): push.** The bs extension runs `bs inbox --wait` in-process and, when it exits 0, calls `pi.sendMessage(<doorbell>, {triggerTurn: true, deliverAs: "followUp"})`; `Now` steers instead. The extension arms the waiter itself on `session_start` and after each exit, so no model has to. This replaces ADR-0009's per-worker socket at `$XDG_RUNTIME_DIR/bees/<name>.sock` and its token file: bs never opens a socket or holds a secret for an omp worker.
- **agy: the waiter**, as #168 builds it, until agy gets a usable push ([Revisit when](#revisit-when)):
  - `bs inbox --wait [--timeout 1h]` holds a per-session flock (`<cache>/<name>-watch-lock`), reads the logbook every 2 s, and exits 0 printing the doorbell (no bodies, nothing marked read) once mail comes that wasn't unread when it started. A second watcher exits 2 ("already armed"), an ended worker or exited harness exits 2, no mail before the timeout exits 12.
  - agy's `bs-mail` hook in the per-start `hooks.json`: **PreInvocation**, at a turn's first model call with no watcher armed, injects a message asking the model to run `bs inbox --wait` in the background; **Stop**, when the turn ended normally and mail is still unread, holds the turn with the doorbell. Re-arming happens at most once per idle.
  - `Ring(agy)` types nothing: it reports `port.ErrArmed` (the watcher's pid will wake it) or `ErrNoRing` (no watcher armed; held until its next turn).
  - The background-work gate skips the armed watcher's pid and its ancestors, so `bs done` and `bs end` aren't refused because of it.

## Why push, where there is one

| | push (Claude Code, omp) | waiter (agy) |
|---|---|---|
| Who wakes the session | bs, deterministically | the model, if it obeyed the hook's "run this in the background" |
| Cost per idle | none | a tool call and a turn to arm and re-arm |
| Latency | the write | up to the 2 s poll, plus agy starting the turn |
| Moving parts in the worker | none (Claude); the extension (omp) | a waiter process, its lock, two hooks, an exemption in the background-work gate |
| What it costs bs | a channel and its auth per harness (Claude's registry socket and `.key`; omp's extension) | one primitive shared by every adapter |

The waiter's one virtue is uniformity: every harness can run it. That isn't worth a model-dependent wake where a deterministic one exists. omp gets both: its push *is* the waiter, run by code instead of by the model. Correctness depends on neither: a missed ring leaves the mail unread in the logbook for the next `bs inbox`.

## Why agy can't take a push today

- `send_message` to an idle interactive session waits until someone next types a prompt: [antigravity-cli#1022](https://github.com/google-antigravity/antigravity-cli/issues/1022), open since 2026-09-14, assigned, no PR or milestone as of agy 1.4.6.
- `agentapi send-message` needs the language server's CSRF token and exits 0 on error, so bs can't tell a delivered ring from a failed one (motif 11).
- Only a background command's exit wakes an idle agy: [concord-mcp#274](https://github.com/Get-Concord-AI/concord-mcp/pull/274) (agy 1.4.4), and seen in our dogfood (#168, agy 1.4.6).
- Remote control (`agy remote-control daemon`, `--remote-control`) can drive a live session, but only through Google's account and servers, with no local API.

## Revisit when

`bs doctor` prints the installed agy version and points here. Re-probe on a new agy release when either holds:

- [#1022](https://github.com/google-antigravity/antigravity-cli/issues/1022) is fixed: `send_message` wakes an idle session;
- agy documents a local API to prompt a live session (remote control or `agentapi`), one that reports failure.

Then the agy adapter's `Ring` becomes a push, and the waiter's hooks (`bs-mail`'s PreInvocation and Stop) and the gate's watcher exemption go. `bs inbox --wait` stays as long as omp's extension uses it.

## Considered Options

- **A waiter for every harness, Claude Code included** (one topology). Rejected: it trades Claude's deterministic, free wake for one that needs the model to arm it and costs a turn per idle, to save one adapter's 20 lines.
- **omp's socket ring (ADR-0009).** Rejected: a socket and a ≥128-bit token per worker (SEC-1) to do what the extension can do in-process with no channel open to anyone.
- **Type into agy's pane (herdr or tmux send-keys).** Rejected as before (R33): no receipt, and text can land in the middle of the keeper's input.

## Consequences

- bees-12.8's Ring is the extension running the waiter, not a listening socket; SEC-1's socket checks don't apply to omp.
- `bs inbox --wait` is a core command with two users (agy's hooks, omp's extension), so its lock, exit codes and doorbell text are tested once in the core.
- An agy worker whose model ignores the arming hook isn't rung; its mail waits for its next turn, and `bs send` says so.
