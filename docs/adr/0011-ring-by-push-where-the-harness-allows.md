# Ring by push where the harness allows it, a waiter where it doesn't

> Amends [ADR-0009](0009-core-and-adapters.md) (omp's ring: the extension runs the waiter, no socket or token per worker; agy's ring: the waiter, not "none until proven") and [ADR-0010](0010-core-rebuild.md)'s `Ring`. Decided by the keeper, 2026-10-23 (bees-13.91), after #168 built agy's waiter. [Amendment 1](#amendment-1-agy-push-by-typing-idle-only-bees-1397) (keeper, 2026-10-23, bees-13.97): agy's ring becomes a push by typing into an idle pane, and the waiter's arming is retired.

A doorbell only changes *when* a session reads its mail, never *whether* (S2, motif 10): `bs send` writes the message to the logbook first, and the worker reads it with `bs inbox`. So the ring can differ per harness without risking a message. It has to, because the harnesses don't offer the same things:

| harness | push from outside into an idle session | a finished in-session waiter wakes it |
|---|---|---|
| Claude Code | ✅ cross-session message (today's ring) | ✅ a finished background Bash starts a turn |
| agy 1.4.6 | ❌ see [Why agy can't take a push](#why-agy-cant-take-a-push-today); typed into an idle pane by herdr instead ([Amendment 1](#amendment-1-agy-push-by-typing-idle-only-bees-1397)) | ✅ only when a background command exits |
| pi / omp | ✅ the bs extension's `pi.sendMessage(…, {triggerTurn: true, deliverAs: "followUp"})` starts a turn when idle and queues one when busy | ✅ the same extension can run the waiter in-process |

**Decision: push where the harness allows it, a waiter where it doesn't.** The core stays one: the logbook, the doorbell text, `bs inbox --wait` and the `bs end` gate. Only each adapter's `Ring` differs.

```text
                ┌──────────── logbook (truth) ────────────┐
 bs send ─store─┤                                         │
                └──────────────────▲──────────────────────┘
                                   │ bs inbox --wait (polls 2 s, one per worker)
     Claude Code            pi / omp                     agy
     Ring = push:           Ring = push:                 Ring = typed (Amendment 1):
     cross-session msg      the bs extension runs        herdr agent prompt into an
     to the recorded pid    bs inbox --wait in-process,  idle pane with an empty box;
                            then pi.sendMessage(         else held, and its Stop hook
                            triggerTurn, followUp)       shows it at the turn's end
```

- **Claude Code: push**, today's cross-session message to the recorded pid (R33, ADR-0009). It stays a push; it does not move to a waiter.
- **pi/omp (bees-12.8): push.** The bs extension runs `bs inbox --wait` in-process and, when it exits 0, calls `pi.sendMessage(<doorbell>, {triggerTurn: true, deliverAs: "followUp"})`; `Now` steers instead. The extension arms the waiter itself on `session_start` and after each exit, so no model has to. This replaces ADR-0009's per-worker socket at `$XDG_RUNTIME_DIR/bees/<name>.sock` and its token file: bs never opens a socket or holds a secret for an omp worker.
- **agy: the waiter**, as #168 builds it, until agy gets a usable push ([Revisit when](#revisit-when)). *Superseded by [Amendment 1](#amendment-1-agy-push-by-typing-idle-only-bees-1397): the ring types into an idle pane; the PreInvocation arming, the Stop hook's re-arm and `ErrArmed` are gone; the Stop hook's hold with the doorbell and `bs inbox --wait` stay.*
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

`bs doctor` prints the installed agy version (checked against `agy.Probed`, bees-13.13.15) and points here. Re-probe on a new agy release when either holds:

- [#1022](https://github.com/google-antigravity/antigravity-cli/issues/1022) is fixed: `send_message` wakes an idle session;
- agy documents a local API to prompt a live session (remote control or `agentapi`), one that reports failure.

- herdr's `agent prompt` refuses while a permission menu is on screen, not only once its status is blocked (drafted for upstream in bees-13.97's PR).

Then the agy adapter's `Ring` becomes a native push, and the screen read and the Stop hook's hold may go (a native push that queues mid-turn needs no hold). `bs inbox --wait` and the gate's watcher exemption stay as long as omp's extension uses them.

## Considered Options

- **A waiter for every harness, Claude Code included** (one topology). Rejected: it trades Claude's deterministic, free wake for one that needs the model to arm it and costs a turn per idle, to save one adapter's 20 lines.
- **omp's socket ring (ADR-0009).** Rejected: a socket and a ≥128-bit token per worker (SEC-1) to do what the extension can do in-process with no channel open to anyone.
- **Type into agy's pane (herdr or tmux send-keys).** Rejected as before (R33): no receipt, and text can land in the middle of the keeper's input. *Taken for agy by Amendment 1, guarded against both: the logbook receipt stays the only proof of reading, and nothing is typed while the input box holds text.*

## Consequences

- bees-12.8's Ring is the extension running the waiter, not a listening socket; SEC-1's socket checks don't apply to omp.
- `bs inbox --wait` is a core command; after Amendment 1 its one user is omp's extension. Its lock, exit codes and doorbell text are tested once in the core.
- ~~An agy worker whose model ignores the arming hook isn't rung~~ (Amendment 1: no model arms anything). An agy worker mid-turn isn't typed into; its Stop hook shows the doorbell when the turn ends, and `bs send` says so.

## Amendment 1: agy, push by typing, idle-only (bees-13.97)

Decided by the keeper, 2026-10-23, on bees-13.95's measurements (245+ rings on real agy 1.4.6 and herdr 0.9.5):

| case | push (`herdr agent prompt`) | waiter (#168) |
|---|---|---|
| idle | 45/45 read, p50 ~3 s | 21/21, p50 7.2 s |
| idle after a cancelled turn (a denied prompt) | 40/40 | **0/20**: deaf (bees-13.94) |
| bursts, 5 rings in 1 s | 60/60, merged into turns | – |
| lost Enter, duplicates, garbling | 0 | – |

**Decision: agy's `Ring` types the doorbell with `herdr agent prompt`, only into an idle pane with an empty input box, until agy has a native push.**

```text
Ring(agy):  herdr agent get <pane>      status idle | done?      no → held: "<w> is working; ... Stop hook"
            herdr pane read <pane>      input box empty, no menu? no → held: "<w>'s input box has text" / "at a permission menu"
            herdr agent prompt <pane> <doorbell>                  agent_blocked → held
```

- **Never into a working or blocked pane.** herdr's blocked status lags agy's permission menu by 0.1-0.2 s, and a ring typed in that gap has its Enter take the menu's default, `1. Yes, run command`: 16 of 16 rings herdr typed in the gap approved the command, and herdr answered ok (bees-13.95). An idle agy shows no menu until a turn starts, so an idle-only ring stays out of that gap. Typing into a working pane would gain nothing anyway: the Stop hook holds the turn with the doorbell while mail is unread.
- **Never into a box holding text.** It would merge with the keeper's half-typed line and submit it (bees-13.95 case 6, 20/20 merged).
- **Only at agy's prompt as it draws it**, read with `herdr pane read --format ansi`, trailing blank rows dropped: the `>` line between the last two rules, agy's idle footer `? for shortcuts` right under it (agy blanks it while someone types and says `esc to cancel` mid-turn), and no menu under the last rule (a `1. Yes` the agent wrote above doesn't count). Grey text in the box (256-colour 8, dim, or a truecolor grey) is agy 1.4.7's placeholder hint (`Accept-edits mode: …`), not typed; it only picks the reason, since the footer decides and text with the footer still fails closed. A view drawn under or over the box (`/tasks`, Task Details) or any screen bs can't read as agy's prompt types nothing. The ring sends only the doorbell and Enter, never navigation keys: an Escape to close an overlay once cancelled a pending permission prompt behind it (queen-bees, 2026-10-23).
- **`bs send` says what happened, never "delivered"** (motif 11): `rung <w>` (keys typed), or `stored, not rung: <w> is <reason>; if it's still unread once <w> is idle, run: bs ring <w>`. It promises no Stop hook: a turn the keeper cancels (bees-13.94) or agy's limits cut runs none, and a box cleared unsent starts no turn.
- **A held ring is the queen's to ring again** (keeper, 2026-10-23, over a detached re-ringer): `bs status` shows `stuck: <w>: idle with N unread, not rung → bs ring <w>` for a live agy worker herdr reports idle or done with mail unread, and `bs ring <w>` runs the same `Ring` once: `rung <w>` (exit 0), held (exit 13, `ExitHeld`, saying why), or refused (exit 2: no unread mail, or the worker ended or stopped). herdr's `agent_prompted` means keys sent, not read: it said ok when the text was only queued, when it answered a menu, and when it merged with someone's text. The logbook receipt is the only proof of reading.
- **Kept**: the Stop hook's hold with the doorbell while mail is unread, `bs inbox --wait` (omp's extension), and the background-work gate's exemption for an armed watcher (omp's extension runs it as a child of the harness).
- **Retired for agy**: the PreInvocation hook that asked the model to arm a watcher (`bs hook --harness agy PreInvocation` stays a silent no-op for one release, for workers spawned before), the Stop hook's re-arm and its asked-once marker, `port.ErrArmed`, and `bs send`'s "its harness has no ring" for agy (bees-13.93).
- This reads the screen, which R38 kept out of attention state: here it decides only whether to type, the logbook stays the truth, and a screen bs can't read as agy's types nothing.

### Risk

The remaining races each need a coincidence inside the ~0.3 s between `agent get` and the keys landing (`agent prompt` itself takes 0.32 s):

- the keeper submits a prompt at the same instant, so the doorbell lands mid-turn, queued or in the box behind their text;
- an idle agy's own background task or schedule timer starts a turn whose first tool call asks permission inside the typing window, and the doorbell's Enter answers that menu.

The message is in the logbook first, so no race loses mail. The worst case is one command approved without a prompt, in a worktree still behind `bs guard` (and GitHub branch protection where enabled; recommended in [SETUP.md](../SETUP.md#worker-permissions), not set on this repo today). The keeper judged it low risk (2026-10-23).

### Revisit agy's typed ring when

The [watch list above](#revisit-when) stands, with herdr refusing `agent prompt` while a permission menu is on screen added: that closes the menu race whatever agy does. See [SETUP.md](../SETUP.md#future-agy-work-that-would-simplify-bs) for what each upstream change lets `bs` delete.
