# Whether a session is alive is derived, never stored

pns never records that a session started, ended or died. Whether it is running is worked out each time from Claude Code's session registry (`~/.claude/sessions/<pid>.json`), checking that the pid is still alive and its start time matches. Dead sessions are routine (closed panes, `/exit`, crashes, reboots). Because unread is computed from the logbook, a dead session needs no cleanup: its messages stay unread, and it is rung when it next runs under the same name. Crew kept a mutable sessions table that had to be repaired after deaths; this removes that whole class of bug.

## Consequences

There are no `spawned`, `ended` or `reaped` entry kinds and no reaping action. `pns status` reports "not running" instead.
