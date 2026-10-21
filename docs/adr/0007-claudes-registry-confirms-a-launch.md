# Claude's registry, not herdr, says whether a worker started

> Amended by [ADR-0009](0009-core-and-adapters.md): herdr's `timeout` still means unconfirmed, but the deciding source is now `herdr pane process-info` showing the harness process in the recorded pane within the wait, whose pid and start time bs then records (amendment 1), for every harness. A recorded pane id avoids the unnamed-agent problem below. Claude's registry is read only to ring.

`herdr agent start` reports whether Claude reached an *idle* prompt in time, which is not the same as "Claude started". A worker's first prompt is its doorbell, so a healthy worker goes straight to work and herdr times out on it (`timeout`, herdr 0.9.3, probed in bees-1.14). The same code comes back when Claude exits at once, as `claude --continue` does when there is no conversation. After a timeout herdr also leaves the agent unnamed, so `herdr agent get <name>` says "not found" while the worker runs.

So bs reads herdr's answers like this:

- `timeout` means *unconfirmed*;
- the worker's liveness comes from Claude's session registry, the same source `bs status` uses (ADR-0004);
- the registry decides both whether an unconfirmed start succeeded (wait up to 20 s for a live session with the worker's name) and whether a spawn would start a twin.

An unreadable registry counts as an error, never as "not running". herdr keeps what only it can do: create and remove worktrees and start the process in a pane. It also still reports `agent_not_ready` (the folder-trust prompt) within about 3 s, and that still counts as started.

herdr refuses any agent argument containing a Unicode control character. The rule is written once, in `bee.CheckAgentArgs`. The CLI adapter applies it before running herdr, the test fake uses the same function, and `TestCheckAgentArgsMatchesRealHerdr` runs the probed cases against the installed herdr; it has no side effects, because herdr checks arguments before it looks up the pane. Text that can span lines, such as the system prompt, goes in a file, and the queen gets the same file as every worker.

## Considered Options

- **Start Claude with no first prompt, then ring the doorbell.** herdr would then see an idle prompt and succeed, and it would name the agent. Rejected for two reasons. It brings back the race with socket registration that ADR-0006 avoided. And a worker stopped at the trust prompt would never get its doorbell, while a first prompt survives the trust prompt.
- **A longer herdr `--timeout`.** Rejected: a working worker is never idle in time, so spawn would block for the whole wait and still fail. bs passes 10 s instead of herdr's default 30 s. That covers a cold start (3–6 s) and the trust prompt (3.4 s), and a busy worker costs 10 s instead of 30.
- **Rename the agent in herdr after confirming it.** `herdr agent rename <pane> <name>` works, but nothing in bs reads herdr's agent names any more. Left out until something needs it.

## Consequences

- A worker herdr timed out on shows no name in `herdr agent list`. Find it with `bs status` (which shows its label) or by its worktree path.
- A restart continues the conversation only if the worker has read a message since this spawn (a `received` entry after the `spawned` one), which is the logbook's evidence that a conversation exists. Otherwise it starts fresh.
