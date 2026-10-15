# One append-only `entries` table, unread computed as a set

The logbook is a single table, `entries`, that is only ever inserted into. Unread is "messages to me with no receipt from me", computed by a query every time. There is no cursor, no time window and no mutable inbox. Every field that is filtered or joined on is a real column (`kind`, `actor`, `subject`, `ref`), and receipts use deterministic ids so a duplicate is a no-op.

## Considered Options

Benchmarked on Dolt 2.4.2 (2026-10, scripts kept in the session scratchpad):

- **A: one table per kind.** Duplicate receipts failed 9–15 of 30 with error 1213, and there is more schema to keep in step.
- **D: read cursor per session.** Silently loses a slow send whose id lands behind the cursor. Crew lost facts the same way with a time window (crew 3572f24).
- **E: log plus a mutable inbox table.** Fastest unread (0.3 ms at 200k), but it is a second copy of the truth. A receive that commits before its send leaves a stray row that needs a repair job.
- **F: log plus a Dolt branch per session as a read marker.** Works, but needs an ancestor guard, deliver-only-up-to-the-ring rules and stale-marker reaping. Branch moves are not transactional, and a backward diff silently returns nothing.

Unread for one session takes 8.5 ms at 20k messages, which is fast enough. If it ever gets slow, E's inbox table can be added as a rebuildable projection without changing anything else.

## Consequences

Every write is its own `DOLT_COMMIT`, with the actor as author, so `dolt_log` is the audit trail. That is about 20 KB per commit before GC, roughly 40 MB a day at expected volume, so there is no GC timer; `pns status` warns above 10 GB. Dolt's auto-GC was seen to stop running, and a full disk crashes the shared server and beads with it.
