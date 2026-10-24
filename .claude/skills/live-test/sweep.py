#!/usr/bin/env python3
"""sweep.py [--days N] [--apply]: list, then with --apply remove, what live tests and bake-off
runs leave behind once their own teardown is done (bees-13.78.20).

Dry run by default. In order:
  1. run dirs older than N days (newest file in the tree; default 7) under $BEES_LIVE
     (lt<16 digits>, after teardown.sh) and $BAKEOFF_RUNS (t<script><arm>[tag]), once
  2. the toy queens of those runs and of runs already gone are stopped: a keeper herdr
     workspace labelled "<toy> · …" whose every pane's cwd is under a test cache, and a
     live-test toy's own herdr server;
  3. toy logbooks (<store>/<toy>.db and its -wal/-shm/.pre-*/.queen.lock) whose run dir is gone,
     proven a toy's: every repo its spawns recorded is the toy's, under a test cache;
  4. toy beads (<toy>-N) whose run dir is gone, each created by a toy (bakeoff, or the toy id),
     unless linked to a bead outside the toy;
  5. bs start files (<session>-<key>-<digits>) whose session has no open record: a worker
     whose last spawned/ended entry is ended (or never spawned), a queen whose toy's logbook
     is gone. Files younger than an hour stay (a spawn in flight writes them first).

A name matching a toy's pattern is not proof: a real project may be named t2cache. Each step
needs its own proof, never touches a configured project, and leaves a toy alone whose queen
or server it couldn't stop (motif 12).
Exit 0: done (or listed). 1: a step failed or couldn't tell; nothing it couldn't prove went.
"""
import argparse, json, os, re, shutil, subprocess, sys, time

HERE = os.path.dirname(os.path.abspath(__file__))
HOME = os.path.expanduser("~")
LIVE = os.path.realpath(os.environ.get("BEES_LIVE") or f"{HOME}/.cache/bees-live")
RUNS = os.path.realpath(os.environ.get("BAKEOFF_RUNS") or f"{HOME}/.cache/bees-bakeoff/runs")
STORE = f"{HOME}/.local/share/bees"
CONFIG = os.path.join(os.environ.get("XDG_CONFIG_HOME") or f"{HOME}/.config", "bees")
CACHE = f"{HOME}/Library/Caches/bees/bs" if sys.platform == "darwin" else os.path.join(os.environ.get("XDG_CACHE_HOME") or f"{HOME}/.cache", "bees/bs")
ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")
LT_RUN = re.compile(r"lt\d{16}")  # a live-test toy id, its run dir and herdr session
BAKE_RUN = re.compile(r"t\d+b?[abc][a-z]*")  # a bake-off run: t<script><arm>[tag]
# A toy bs project: a live-test toy id (plus a letter suffix a check may add) or a bake-off arm c run.
TOY = re.compile(r"(lt\d{16})[a-z]*|(t\d+b?c[a-z]*)")
START_FILE = re.compile(r"(queen-([a-z0-9]+)|([a-z0-9]+)(?:-\d+)+)-[a-z][a-z-]*-\d+(?:\.[a-z]+)?")
HOUR = 3600

failed = False
REAL = set()  # configured projects, read in main
HELD = set()  # run dirs of toys still running: nothing of theirs goes


def warn(msg):
    global failed
    failed = True
    print(f"error: {msg}", file=sys.stderr)


def configured():
    """Projects with a config file are real, whatever their name."""
    dirs = [f"{CONFIG}/projects"] + [f"{CONFIG}/sessions/{s}/projects" for s in (os.listdir(f"{CONFIG}/sessions") if os.path.isdir(f"{CONFIG}/sessions") else [])]
    return {f[:-5] for d in dirs if os.path.isdir(d) for f in os.listdir(d) if f.endswith(".toml")}


def toy_run(project):
    """The run dir a toy project belongs to, or None for a name that isn't a toy's."""
    m = TOY.fullmatch(project)
    if not m or project in REAL:
        return None
    return f"{LIVE}/{m[1]}" if m[1] else f"{RUNS}/{m[2]}"


def newest(path):
    t = os.lstat(path).st_mtime
    for d, dirs, files in os.walk(path):
        for n in dirs + files:
            try:
                t = max(t, os.lstat(os.path.join(d, n)).st_mtime)
            except FileNotFoundError:
                pass
    return t


def size(path):
    n = 0
    for d, _, files in os.walk(path):
        for f in files:
            try:
                n += os.lstat(os.path.join(d, f)).st_size
            except FileNotFoundError:
                pass
    return n


def procs_in(path):
    """Pids whose cwd is in path; None where there is no /proc to tell."""
    if not os.path.isdir("/proc"):
        return None
    pids = []
    for p in os.listdir("/proc"):
        if p.isdigit():
            try:
                cwd = os.readlink(f"/proc/{p}/cwd")
            except OSError:
                continue
            if cwd == path or cwd.startswith(path + "/"):
                pids.append(p)
    return pids


def remove(path, root, pattern, apply):
    """Remove path, named after a toy, from root: refuse anything else."""
    name = ANSI.sub("", os.path.basename(path))
    if os.path.dirname(path) != root or not pattern.fullmatch(name):
        warn(f"refusing to remove {path!r}: not a toy's entry of {root}")
        return False
    if not apply:
        return True
    try:
        if os.path.isdir(path) and not os.path.islink(path):
            shutil.rmtree(path)
        else:
            os.remove(path)
    except OSError as e:
        warn(f"remove {path!r}: {e}")
        return False
    return True


def run(*cmd):
    return subprocess.run(cmd, capture_output=True, text=True, timeout=60)


def herdr_json(*args):
    env = {k: v for k, v in os.environ.items() if k != "HERDR_SESSION"}  # the keeper's server
    r = subprocess.run(["herdr", *args], capture_output=True, text=True, timeout=30, env=env)
    if r.returncode:
        raise RuntimeError(f"herdr {' '.join(args)}: {r.stderr.strip() or r.returncode}")
    return json.loads(r.stdout)


def stale_runs(days, live_toys):
    """Step 1: run dirs untouched for days, torn down, as (path, root, pattern, stray)."""
    out, cutoff = [], time.time() - days * 86400
    for root, pattern in ((LIVE, LT_RUN), (RUNS, BAKE_RUN)):
        if not os.path.isdir(root):
            continue
        for name in sorted(os.listdir(root)):
            path, clean = os.path.join(root, name), ANSI.sub("", name)
            if not pattern.fullmatch(clean) or not os.path.isdir(path) or os.path.islink(path):
                continue
            stray = clean != name  # an ls-coloured name fed back as a path (bees-13.76)
            if newest(path) > cutoff:
                continue
            if stray:  # teardown.sh refuses its name: it goes only without a toy repo in it
                if os.path.exists(f"{path}/{clean}"):
                    print(f"keep {path!r}: a toy repo in a dir teardown.sh refuses; tear it down by hand")
                else:
                    out.append((path, root, pattern, stray))
                continue
            if root == LIVE and (os.path.exists(f"{path}/{name}") or os.path.isdir(f"{CONFIG}/sessions/{name}")):
                print(f"keep {path}: not torn down; run teardown.sh {path}")
                continue
            if name in live_toys:
                print(f"keep {path}: its herdr server {name} still runs; run teardown.sh {path}")
                continue
            out.append((path, root, pattern, stray))
    return out


def remove_runs(apply, stale, stopped):
    """Step 1, once their toy queens are stopped: remove the stale run dirs no process runs in.
    Returns the run dirs gone after it (removed, or would be)."""
    gone = set()
    for path, root, pattern, stray in stale:
        pids = procs_in(path)
        for _ in range(20 if apply and path in stopped else 0):  # a closed pane's processes take a moment to exit
            if not pids:
                break
            time.sleep(0.5)
            pids = procs_in(path)
        if pids and not (path in stopped and not apply):
            print(f"keep {path!r}: processes {' '.join(pids)} run in it")
            continue
        mb = size(path) / 1e6
        if remove(path, root, pattern, apply):
            print(f"{'removed' if apply else 'would remove'} {'stray ' if stray else ''}run dir {path!r} ({mb:.1f} MB)")
            if not stray:  # the stray's real twin may still be there
                gone.add(path)
    return gone


def gone_run(project, swept):
    r = toy_run(project)
    return r is not None and r not in HELD and (r in swept or not os.path.exists(r))


def under(path, root):
    return bool(path) and os.path.realpath(path).startswith(root + "/")


def sweep_queens(apply, stale):
    """Step 2: toy queens (and bake-off leads) in the keeper's herdr whose run dir is stale or gone.
    Returns the run dirs whose queen it stopped (or would)."""
    stopped = set()
    try:
        wss = herdr_json("workspace", "list")["result"]["workspaces"]
        panes = herdr_json("pane", "list")["result"]["panes"]
    except (RuntimeError, ValueError, KeyError, OSError) as e:
        warn(f"can't list the keeper's herdr workspaces, so nothing is swept: {e}")
        return None
    for ws in wss:
        toy = (ws.get("label") or "").split(" · ")[0]
        if " · " not in (ws.get("label") or "") or toy in REAL:
            continue
        r = toy_run(toy) or (f"{RUNS}/{toy}" if BAKE_RUN.fullmatch(toy) else None)  # arm a/b leads have no project
        if r is None or not (r in stale or not os.path.exists(r)):
            continue
        mine = [p for p in panes if p.get("workspace_id") == ws["workspace_id"]]
        if not mine or not all(under(p.get("cwd"), r) and under(p.get("foreground_cwd") or p.get("cwd"), r) for p in mine):
            print(f"keep workspace {ws['workspace_id']} ({ws['label']}) and {toy}'s leftovers: a pane's cwd is outside {r}")
            HELD.add(r)
            continue
        if apply:
            res = run("herdr", "workspace", "close", ws["workspace_id"])
            if res.returncode:
                warn(f"herdr workspace close {ws['workspace_id']}: {res.stderr.strip()}")
                HELD.add(r)
                continue
        print(f"{'stopped' if apply else 'would stop'} toy queen: workspace {ws['workspace_id']} ({ws['label']})")
        stopped.add(r)
    return stopped


def toy_servers():
    """Running herdr sessions named after a live-test toy."""
    try:
        ss = herdr_json("session", "list", "--json")["sessions"]
    except (RuntimeError, ValueError, KeyError, OSError) as e:
        warn(f"can't list herdr sessions: {e}")
        return None
    return {s["name"] for s in ss if LT_RUN.fullmatch(s["name"])}


def sweep_servers(apply, servers):
    """Step 2b: a live-test toy's own herdr server whose run dir is gone, every pane of it in that
    run dir (a live test under another $BEES_LIVE has its panes elsewhere)."""
    for name in sorted(servers):
        r = f"{LIVE}/{name}"
        if os.path.exists(r):
            continue
        try:
            panes = herdr_json("--session", name, "pane", "list")["result"]["panes"]
        except (RuntimeError, ValueError, KeyError, OSError) as e:
            print(f"keep herdr server {name} and its leftovers: can't list its panes ({e})")
            HELD.add(r)
            continue
        if not all(under(p.get("cwd"), r) for p in panes):
            print(f"keep herdr server {name} and its leftovers: a pane's cwd is outside {r}")
            HELD.add(r)
            continue
        if apply:
            stop = run("herdr", "--session", name, "server", "stop")
            delete = run("herdr", "session", "delete", name)
            if delete.returncode:
                warn(f"herdr session delete {name}: {delete.stderr.strip() or stop.stderr.strip()}")
                HELD.add(r)
                continue
        print(f"{'stopped' if apply else 'would stop'} toy herdr server {name}")


def sweep_logbooks(apply, swept):
    """Step 3: a toy's logbook files in the store, once its run dir is gone. Returns the projects."""
    gone = set()
    if not os.path.isdir(STORE):
        return gone
    file = re.compile(r"(.+?)(\.db(-wal|-shm|\.pre-\d+)?|\.queen\.lock)")
    by = {}
    for f in sorted(os.listdir(STORE)):
        m = file.fullmatch(f)
        if m and gone_run(m[1], swept):
            by.setdefault(m[1], []).append(f)
    for project, files in by.items():
        if f"{project}.db" in files and not proven(f"{STORE}/{project}.db", project):
            print(f"keep logbook {project}: named like a toy, but its spawns don't prove it one")
            continue
        if all(remove(f"{STORE}/{f}", STORE, re.compile(re.escape(project) + r"\..+"), apply) for f in files):
            print(f"{'removed' if apply else 'would remove'} logbook {project}: {' '.join(files)}")
            gone.add(project)
    return gone


def proven(db, project):
    """Whether db is a toy's logbook: it recorded a spawn, and every spawn's repo is <project>
    in a bake-off or live-test cache (rounds used other $BAKEOFF_RUNS, so any such cache)."""
    r = run("python3", "-I", f"{HERE}/logbook.py", db, "SELECT coalesce(json_extract(data, '$.workspace.repo'), json_extract(data, '$.repo'), '') FROM entries WHERE kind = 'spawned'")
    repos = r.stdout.split("\n")[:-1] if r.returncode == 0 else []
    cache = re.compile(r"/(bees-bakeoff|bees-live)[^/]*/(.+/)?" + re.escape(project) + "/" + re.escape(project))
    return bool(repos) and all(cache.search(p) and p.endswith("/" + project) for p in repos)


def sweep_beads(apply, swept):
    """Step 4: every <toy>-* bead whose run dir is gone, unless linked to a bead outside the toy."""
    r = run("bd", "list", "--all", "--limit", "0", "--json")
    if r.returncode:
        warn(f"bd list: {r.stderr.strip()}")
        return
    by, foreign = {}, {}
    for b in json.loads(r.stdout or "[]"):
        project = b["id"].split("-")[0]
        if gone_run(project, swept):
            # mkrun.sh sets the toy repo's git user to bakeoff, setup.sh to the toy id
            ok = b.get("created_by") in ("bakeoff", TOY.fullmatch(project)[1])
            (by if ok else foreign).setdefault(project, []).append(b["id"])
    for project in sorted(foreign):
        print(f"keep beads {project}-*: {len(foreign[project])} not created by a toy ({' '.join(foreign[project][:3])}…)")
        by.pop(project, None)
    for project, ids in sorted(by.items()):
        show = run("bd", "show", *ids, "--json")
        if show.returncode:
            warn(f"bd show {project}-*: {show.stderr.strip()}")
            continue
        links = [f"{b['id']} ↔ {d['id']}" for b in json.loads(show.stdout) for d in (b.get("dependencies") or []) + (b.get("dependents") or []) if not d["id"].startswith(project + "-")]
        if links:
            print(f"keep beads {project}-*: linked outside the toy ({', '.join(links)}); remove those links first (bd dep remove)")
            continue
        if apply:
            d = run("bd", "delete", "--force", *ids)
            if d.returncode:
                warn(f"bd delete {project}-*: {d.stderr.strip()}")
                continue
        print(f"{'deleted' if apply else 'would delete'} {len(ids)} beads {project}-*")


def last_record(db, name):
    """The kind of name's latest spawned/ended entry in db ('' for none); None if unreadable."""
    r = run("python3", "-I", f"{HERE}/logbook.py", db, "SELECT kind FROM entries WHERE subject = ? AND kind IN ('spawned', 'ended') ORDER BY ts DESC, rowid DESC LIMIT 1", name)
    return r.stdout.strip() if r.returncode == 0 else None


def sweep_start_files(apply, swept, logbooks):
    """Step 5: bs start files whose session has no open record."""
    if not os.path.isdir(CACHE):
        return
    now, sessions = time.time(), {}
    for f in sorted(os.listdir(CACHE)):
        m = START_FILE.fullmatch(f)
        if m and now - os.lstat(f"{CACHE}/{f}").st_mtime > HOUR:
            sessions.setdefault((m[1], m[2] or m[3], bool(m[2])), []).append(f)
    for (name, project, queen), files in sorted(sessions.items()):
        db = logbook_of(project)
        if project in logbooks or (gone_run(project, swept) and not os.path.exists(db)):
            why = "its toy's logbook is gone"
        elif not os.path.exists(db):
            print(f"keep {name}'s start files: no logbook {db} to tell, and {project} isn't a toy")
            continue
        elif queen:
            continue  # a project's queen is open while its logbook is
        else:
            kind = last_record(db, name)
            if kind is None:
                warn(f"couldn't read {db}, so {name}'s start files stay")
                continue
            if kind == "spawned":
                continue
            why = "ended" if kind else "never spawned"
        pattern = re.compile(re.escape(name) + r"-[a-z][a-z-]*-\d+(\.[a-z]+)?")
        if all(remove(f"{CACHE}/{f}", CACHE, pattern, apply) for f in files):
            print(f"{'removed' if apply else 'would remove'} {name}'s start files ({why}): {' '.join(files)}")


def logbook_of(project):
    """A project's logbook: in the store, or for a live-test toy in its run dir (setup.sh's logbook_dir)."""
    db = f"{STORE}/{project}.db"
    m = TOY.fullmatch(project)
    return f"{toy_run(project)}/logbooks/{project}.db" if m and m[1] and toy_run(project) and not os.path.exists(db) else db


def main():
    global REAL
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0], formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--days", type=float, default=7, help="remove run dirs untouched this long (default 7)")
    ap.add_argument("--apply", action="store_true", help="remove; without it, only list (dry run)")
    a = ap.parse_args()
    REAL = configured()
    print(f"{'sweeping' if a.apply else 'dry run (--apply removes)'}: run dirs older than {a.days:g} days in {LIVE} and {RUNS}")
    servers = toy_servers()
    if servers is None:
        return 1  # can't tell which toys still run: fail closed
    stale = stale_runs(a.days, servers)
    stopped = sweep_queens(a.apply, {p for p, _, _, stray in stale if not stray})
    if stopped is None:
        return 1
    sweep_servers(a.apply, servers)
    swept = remove_runs(a.apply, [s for s in stale if s[0] not in HELD], stopped)
    logbooks = sweep_logbooks(a.apply, swept)
    sweep_beads(a.apply, swept)
    sweep_start_files(a.apply, swept, logbooks)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
