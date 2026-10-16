# Reconcile on triggers, not a daemon

From v1, all "what should happen now" logic (ring, re-ring, flag the queen) is a pure function, `Plan(facts, now) []Action`. It runs whenever bs is touched: on `bs send`, every Claude hook event (debounced to 5 s), `bs status` and `bs inbox`, plus a short detached watchdog that each send leaves behind. There is no `bs tick`, `bs wait` or supervisor process to keep alive, because the captain and sessions touch bs constantly. Reconciles are serialized by SQLite's write lock (`BEGIN IMMEDIATE`), held from reading the facts to recording what was done. A crashed holder releases it automatically.

## Consequences

- v0 has no reconciler at all; it rings on every send.
- Holding the write lock while ringing blocks other writers for the length of a socket write (milliseconds), which the busy timeout covers.
