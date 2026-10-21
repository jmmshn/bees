# Whether a session is alive is derived, never stored

> Amended by [ADR-0009](0009-core-and-adapters.md): still derived, never stored, but from the harness process recorded at start (pid alive with its recorded start time), for every harness, not from Claude's registry or herdr's agent report (amendment 1).

bs never records whether a session is running. It works that out each time:

- from Claude Code's session registry (`~/.claude/sessions/<pid>.json`), checking that the pid is still alive and its start time matches, when ringing;
- from herdr (`herdr agent get <name>`) when deciding whether a spawned bee is live.

Dead sessions are routine: closed panes, `/exit`, crashes, reboots. Because unread is computed from the logbook, a dead session needs no cleanup. Its messages stay unread, and it is rung when it next runs under the same name. Crew kept a mutable sessions table that had to be repaired after deaths; this removes that whole class of bug.

## Amendment (2026-10-15, ADR-0006)

`spawned` and `ended` entries exist, but they record what bs created and tore down, never whether a session is alive:

- `spawned` holds the herdr workspace id, which `bs end` needs once the agent is gone;
- `ended` closes it out.

An open `spawned` entry says "this workspace belongs to this bee", not "this bee is running".
