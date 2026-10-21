# The logbook is a SQLite file, not a Dolt database

> Amended by [ADR-0006](0006-spawn-and-naming.md) (one logbook per project), [ADR-0008](0008-config-files-one-tagged-struct.md) (its directory is `paths.logbook_dir`) and R47 (macOS too).

The logbook is `<paths.logbook_dir>/<project>.db` (default `~/.local/share/bees/<project>.db`), one file per project, opened in WAL mode through the pure-Go `modernc.org/sqlite` driver (the same choice as crew). Writes use `BEGIN IMMEDIATE` with a busy timeout. All sessions run on one machine (Linux or macOS), and bs uses none of what makes Dolt worth having (branch, diff, merge, clone). What remained was a plain table plus the costs of a server.

## Considered Options

- **A database on the shared Dolt sql-server that beads uses.** This was the original design, chosen to reuse infrastructure. Benchmarking (2026-10, Dolt 2.4.2) turned up several costs:
  - retries on 1213 conflicts for concurrent writers;
  - an undocumented `GET_LOCK` needed for mutual exclusion;
  - about 20 KB of journal per commit, with auto-GC that stopped running;
  - a full disk crashing the server, and beads with it.
- **Append-only JSONL files, Claude Code's own pattern.** That works when each file has one writer. The logbook has many writers and is queried across everything, so it would mean hand-building locking, atomic writes, indexes and deduplication.

## Consequences

- Other machines can't share the logbook, and it can't be joined against beads tables. If cross-machine history is ever needed, copy `entries` into Dolt then.
- No conflict retries, no named locks and no GC policy. The write lock is the only concurrency control.
