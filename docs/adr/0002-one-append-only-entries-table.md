# One append-only `entries` table, unread computed as a set

> Amended by [ADR-0009](0009-core-and-adapters.md) (amendment 1): the `kind` CHECK widens once, in one table rebuild, for `queen`, `started` and `done`; unread is messages and `done` entries addressed to me with no receipt from me.

The logbook is a single table, `entries`, that is only ever inserted into. Unread is "messages to me with no receipt from me", computed by a query every time. There is no cursor, no time window and no mutable inbox. Every field that is filtered or joined on is a real column (`kind`, `actor`, `subject`, `ref`), and receipts use deterministic ids so a duplicate is a no-op.

## Considered Options

Benchmarked on Dolt 2.4.2 in 2026-10, before ADR-0005 moved the logbook to SQLite. The data-model findings don't depend on the engine.

- **A: one table per kind.** More schema to keep in step. On Dolt, duplicate receipts also conflicted.
- **D: read cursor per session.** Silently loses a slow send whose id lands behind the cursor. Crew lost facts the same way with a time window (crew 3572f24).
- **E: log plus a mutable inbox table.** The fastest unread query, but a second copy of the truth. A receive that commits before its send leaves a stray row that needs a repair job.
- **F: per-session read markers** (Dolt branches). Needs an ancestor guard, deliver-only-up-to-the-ring rules and stale-marker reaping, and fails silently when misused.

If unread ever gets slow, E's inbox table can be added as a rebuildable projection without changing anything else.
