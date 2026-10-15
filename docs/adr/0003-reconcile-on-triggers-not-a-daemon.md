# Reconcile on triggers, not a daemon; lock with Dolt's GET_LOCK

From v1, all "what should happen now" logic (ring, re-ring, flag the chief) is a pure function, `Plan(facts, now) []Action`. It runs whenever pns is touched: on `pns send`, every Claude hook event (debounced to 5 s), `pns status` and `pns inbox`, plus a short detached watchdog that each send leaves behind. There is no `pns tick`, `pns wait` or supervisor process to keep alive, because the captain and sessions touch pns constantly. One reconcile runs at a time, using MySQL's named lock `GET_LOCK('pns:reconcile', 0)`. Dolt supports it, and the lock is released automatically when the holder's connection drops. It replaces a flock file.

## Consequences

- The lock belongs to one database connection, so `WithLock` must pin a single `*sql.Conn` for its whole life and never use the connection pool.
- If the TCP connection drops while the process lives on, two reconciles can overlap. The worst outcome is a duplicate doorbell, which is harmless.
- `GET_LOCK` is not in Dolt's docs, so an integration test pins its behaviour.
- v0 has no reconciler at all; it rings on every send.
