#!/usr/bin/env python3
"""logbook.py <db> <sql> [param...]: run a read-only query on a bs logbook; rows print tab-separated.

It reads a private copy, never the logbook itself: opening a live WAL logbook, even with
mode=ro, can leave -wal/-shm files or fail on an idle one. The copy is taken only when the
database and its -wal are unchanged across it, so a checkpoint mid-copy can't tear it.
Exit 1: the logbook kept changing, or can't be read.
"""
import os, shutil, sqlite3, sys, tempfile, time


def state(path):
    return [(os.stat(p).st_size, os.stat(p).st_mtime_ns) if os.path.exists(p) else None for p in (path, path + "-wal")]


def main():
    db, sql, params = sys.argv[1], sys.argv[2], sys.argv[3:]
    with tempfile.TemporaryDirectory() as tmp:
        copy = os.path.join(tmp, "l.db")
        for _ in range(20):
            before = state(db)
            shutil.copyfile(db, copy)
            if before[1] is not None:
                shutil.copyfile(db + "-wal", copy + "-wal")
            elif os.path.exists(copy + "-wal"):
                os.remove(copy + "-wal")
            if state(db) == before:
                break
            time.sleep(0.2)
        else:
            sys.exit(f"logbook.py: {db} kept changing; try again")
        for row in sqlite3.connect(copy).execute(sql, params):
            print("\t".join(str(v) for v in row))


if __name__ == "__main__":
    main()
