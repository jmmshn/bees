# The logbook is the only truth; messages are addressed, not broadcast

Every message and fact lives in the logbook, a database (`peons`) on the shared Dolt sql-server that beads already uses. A doorbell sent over Claude Code's cross-session socket carries only "you have unread messages", never a message body, so a lost or duplicated doorbell can never lose or corrupt a message. Only an entry with a recipient wakes anyone; there is no publish/subscribe bus. Crew's ledger note reached the same conclusion ("the log is the bus"): a separate bus is a second truth that can disagree with the log.

## Considered Options

- **Event bus with subscriptions.** Rejected: adds a second source of truth and a broker to keep running. If topics are ever needed, a `subscriptions` table can turn topic entries into addressed messages inside the logbook.
- **Message bodies over the socket.** Rejected: the socket queue holds at most 50 messages, drops identical repeats, and keeps nothing after the session exits.

## Consequences

`pns send` writes the entry first, then rings. A send whose ring fails is still delivered on the next ring.
