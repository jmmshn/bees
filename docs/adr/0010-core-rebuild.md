# The core rebuild: goals fixed at spawn, facts found by bs

> Supersedes [ADR-0009](0009-core-and-adapters.md) where they differ, and takes in the pilot's amendment 1 (on `origin/pilot/bees-12` only). Amends [ADR-0002](0002-one-append-only-entries-table.md) (new entry kinds), [ADR-0004](0004-liveness-is-derived-not-stored.md), [ADR-0006](0006-spawn-and-naming.md), [ADR-0007](0007-claudes-registry-confirms-a-launch.md) and [ADR-0008](0008-config-files-one-tagged-struct.md). Epic bees-13, bead bees-13.1. Settled with the keeper over four plannotator rounds and chat, 2026-10-21 to 10-22.

bees-13 rebuilds bs's core on `main`, using the bees-12 pilot (`origin/pilot/bees-12 @ 75e0f03`, never merged) as fact-finding, so bees runs on other harnesses (agy, omp) and other VCS setups (jj, hg, a CitC client and CLs). Two rules came out of the review and shape every part:

- **Rule D, derive, don't ask** (keeper, round 1): a command takes from its caller only what bs can't look up. Evidence comes from the record and is written into the entry; a value someone typed is never evidence on its own.
- **Rule G, fail fast, fail loud** (keeper, round 3): refuse at the earliest step that can know; print every refusal, stuck state and fallback with the next command to run; a failure never exits 0, and "couldn't look" is never reported as "no" (motif 11).

## Core state

The logbook stays the only truth (ADR-0001): one append-only `entries` table, unread as an anti-join over receipts (ADR-0002), SQLite (ADR-0005), reconciled on triggers (ADR-0003). The kinds:

| kind | written by | holds |
|---|---|---|
| `queen` | `bs queen` | harness, herdr session and pane, pid, start time, exe |
| `spawned` | `bs spawn` | bead, label, harness, herdr `{session, workspace, pane}`, workspace `{kind, dir, branch, base}`, the resolved goal, config dir, harness `record` |
| `started` | start confirmation, or a heal | pid, start time, exe, the baseline of descendants, `late` |
| `goal` | `bs goal` (an amend or a waiver) | the new resolved goal, or the waived check |
| `done` | `bs done` (the handoff) | the PRs handed off so far, or a verdict; every check's result |
| `landed` | `bs end`, `bs prune`, or a `done` | the complete checks that passed; `by: end \| prune \| done` |
| `ended` | `bs end` | `force`, `incomplete` |
| `message`, `received` | `bs send`, `bs inbox` | as today |

- **Liveness** is the recorded harness process alive with its recorded start time, for every harness (amends ADR-0004). herdr's agent status is never read.
- **Identity** is the pane: (`HERDR_SESSION`, or `default` when unset; `HERDR_PANE_ID`) → the open entry that recorded it, accepted only while its recorded process is alive and an ancestor of the calling bs. The session is part of the key because a `herdr --session` server numbers its panes from `w1:p1` (probed, bees-13.1), and every later herdr call for a spawn goes to `herdr --session <recorded>`. bs sets no `BS_*` in a session's env; inherited ones are ignored inside a recorded pane, with a warning. Outside one (tests, scripts, the keeper's shell), `--project`, `--as` and the `BS_*` variables apply as before.
- **Start confirmation** polls `herdr pane process-info` for the harness process within 20 s and takes the baseline at first sight (amends ADR-0007). A start that confirmation missed is healed on any later trigger when the harness process in the recorded pane has `--name <worker>` in its argv; the `started` entry is marked `late`, and `bs status` shows it.
- **The background-work gate** is a built-in check, `no_background`, that every goal's handoff carries implicitly and `bs end` also runs: descendants not in the baseline, minus the caller's ancestors and process group (bees-12.50, 12.52), still alive a second later. Exit 5; can't check, exit 6. `--force` waives it and nothing else. A process that detaches escapes it: a speed bump, said once per spawn.
- **Migration.** One table rebuild takes main's v0 and the pilot's v1 (bees.db already is) to v2, after copying the file aside; the kind CHECK only ever grows (it keeps `rang`). Spawns written before goals read as `--goal legacy` and keep their old data mapped explicitly (`worktree` → `workspace.dir`, `workspace` → `herdr.workspace`).

## The Harness port

```go
type Harness interface {
	Kind() string                                 // herdr --kind and the process name in the pane: claude | omp | agy
	Prepare(st Start) (Launch, error)             // pure: argv, per-start files, guards rendered, Record
	Ring(t Target, text string, p Priority) error // ErrNotRunning | ErrNoRing | a write error
}
```

The pilot's three methods, kept. Core owns the name, the directory, the assembled prompt, writing and removing per-start files (`<name>-[a-z]*-*`), the 768-byte launch check, confirmation and its heal, liveness, identity, the gate, the harness-neutral guard rules, and the switch refusal. A harness that resumes by id gets an id bs chooses, through `Prepare`'s `Record`, stored in `spawned`. If agy's probe (bees-12.12) shows it can't start under a given id, a fourth method, `Observe`, returns post-start facts into the start's single `started` entry; it is added then, not before.

`harness` is one global key: one harness per machine; each entry records its own, and ringing and teardown use the recorded one. ADR-0009's adapter requirements stand: resume the recorded conversation or fail visibly; `Now`/`Next` map to an interrupt and an after-turn; guards load or the start is refused (agy trusts a folder only by the keeper's hand: under `git_worktree` and `command` every spawn is a new folder, so bees-12.12 probes whether trusting the parent covers them); never a skip-permissions flag; SEC-1, 2 and 4 and tests T1–T5, T7–T11.

## Workspaces and VCS

The kind is worked out, not set (Rule D): `[workspace.command]` present → `command`; else the project dir is the top level of a git checkout → `git_worktree`; else `none`. `workspace.kind` remains as an override, and `bs doctor` warns on `none` over a git checkout (bees-12.28).

```toml
[workspace.command]                 # its presence makes the kind command
create  = ["jj", "workspace", "add", "{dir}", "--name", "{name}"]   # cwd: paths.repo
remove  = ["jj", "workspace", "forget", "{name}"]
clean   = ["sh", "-c", "test -z \"$(jj diff --summary)\""]           # required: exits 0 when nothing would be lost
dir     = "{repo}-WS/{name}"
timeout = "60s"
```

`command` brings back what ADR-0009 round 3 and amendment 1 cut (a command kind, init/finalize hooks): goals now make landing a recorded fact, so the directory can be made and removed on facts too. A failed or timed-out `create` runs `remove` and records nothing; a later spawn refuses a `dir` that exists with no entry. VCS knowledge lives in three places: prompt fragments (per workspace kind and per goal type) say how to commit and submit; checks (built in for git and gh, `cmd:` for the rest) say whether the work is there and has landed; bs code makes directories only for `git_worktree` (via herdr) and runs the keeper's argv for `command`. git + GitHub needs no config; jj, hg and a CitC client use `command` plus a custom goal type.

## Goals

The queen fixes the goal at spawn; `--goal` is required.

```text
bs spawn --goal pr                    bees-13.4   # complete when merged (default)
bs spawn --goal pr:ready              bees-13.5   # complete at handoff: open, CI green
bs spawn --goal pr:draft              bees-13.6   # complete at handoff; may close unmerged
bs spawn --goal push=<pr-url>         fs-2.14     # also push:ready=, push:draft=
bs spawn --goal review=<pr-url>       dotfiles-6.6
bs spawn --goal verdict | note | beads | branch | branch:ready | <custom>
bs spawn --goal push=<url> --goal pr  pmgad-2.2   # mixed: every part must hold
```

Grammar `<type>[:<state>][=<target>]`; the target is everything after the first `=`.

| type | the worker gives at `bs done` | handoff: bs checks | complete (`landed`) | prune rule |
|---|---|---|---|---|
| `pr` | `--pr <url>` (repeatable), `--replaces <url>` | each PR open, not a draft, holds a workspace commit, pushed, current with its own base, CI green | every handed-off PR merged | merged |
| `pr:ready` | same | same | at handoff | every PR merged |
| `pr:draft` | same | each PR exists, holds a workspace commit, pushed, CI green (keeper, 2026-10-22) | at handoff | every PR merged or closed |
| `push=<url>` | — | the target's head moved to a commit descending from a workspace commit; current; CI | target merged | as `pr` |
| `review=<url>` | — | a comment on the target since spawn carrying `bs:<spawn id>` | at handoff | at once |
| `verdict` | `--answer yes\|no --report <text>\|-` | both present | at handoff | at once |
| `note` | — | the bead's notes or design differ from the hash taken at spawn | at handoff | at once |
| `beads` | — | a bead created since spawn under the bead | at handoff | at once |
| `branch` | — | the recorded branch ahead of base, clean | merged into base (the pilot's three proofs) | same |
| `legacy` | (spawns from before goals) | — | — | the pilot's three proofs |

- **The worker's only freedom** at exit is its PR URLs, or a verdict. bs checks each URL: its commit range must hold a **workspace commit**, one made in the recorded workspace since the first spawn that recorded the directory (reflog entries that write a commit, never checkout, reset, pull or a fast-forward). A typo can't name someone else's PR. Handed-off PRs accumulate across `bs done` calls; `--replaces` drops one.
- **CI green**: `gh pr checks` all pass; none registered while the base's tip has checks is pending; a vacuous pass is printed.
- **The bead at spawn.** Every `spawned` also freezes its bead (`bead_at`: a hash of its notes and design, and the beads under it), whatever the goal, so a goal amended to `note` or `beads` checks against the spawn. Only a verdict takes `--answer`/`--report`; any other goal refuses them, naming where its words go (bees-13.7).
- **Frozen.** `spawned` holds the resolved goal: every check, each `cmd:` argv and timeout, the fragment's hash. `bs done`, `bs end` and prune read it (or the newest `goal` entry), never config.
- **Amend.** The queen or keeper may amend a live, incomplete goal (`bs goal <name> <value>`, `bs goal <name> --waive <check>`); only the keeper, from a shell with no recorded queen or worker among its ancestors, may set `discard`, at any time. Refused from a worker's descendants, after `landed`, and after `ended` except a discard. An amend re-runs the spawn-time refusals for the new goal.
- **`bs done`** stops at the first check that can't run (exit 6) and otherwise runs every handoff check and reports every failure at once (exit 7). `bs check <name>` runs the handoff checks now and writes nothing; the queen runs it before merging.
- **`bs end`** is the completion gate: complete → `landed`, then `ended`; not complete → exit 9, naming each PR and its state; `--incomplete` ends it anyway (refused when nothing was handed off) and prune completes it later. It closes the herdr workspace and stops the process but keeps the directory.
- **`bs prune`** removes a workspace only when its frozen prune rule passes (or it is discarded) and it has no uncommitted or untracked files, naming what it kept (exit 3, or 4 when it couldn't check). Closing the bead stays with the queen or keeper (motif 5).
- **Custom types** live in `[goal.<name>]` (`handoff`, `complete`, `prune`, `fragment`). A `cmd:` check is argv from config, never a shell; placeholders come only from the record and from checked handoff values, each filling one whole argument; a value starting with `-` is refused and a ref passes `git check-ref-format`; cwd is the recorded dir, env only `PATH`, `HOME`, `LANG`; a timeout kills the process group. Commands come only from the config files, never from bead text, labels or a worker.

The types come from 151 `done` messages in the five real logbooks: about 110 single PRs, eight beads with stacked or follow-up PRs, three replaced PRs, 15 "updated" re-sends, and the rest notes, verdicts, reviews and reports. "Blocked" is never a `done`: the worker messages the queen.

## Config

| key | file | decides |
|---|---|---|
| `harness` | global | which program starts in the pane |
| `paths.logbook_dir`, `paths.repos` | global | where logbooks live; where other projects' repos are |
| `paths.repo`, `paths.worktrees` | project / both | the project dir; `<worktrees>/<name>` |
| `workspace.kind` (override), `[workspace.command]` | both | tab above |
| `[goal.<name>]`, `[checks.<name>]` | both | custom types and their argv |
| `[guards] deny`, `allow_merge_into` | both | deny globs, added to the default; the merge grant (refused unless the base is protected) |
| `[queen.<harness>]`, `[worker.<harness>]`, `worker.path` | both / project | the harness's own knobs; dirs first on a worker's PATH |

Both files live under the config dir, `~/.config/bees`; no repo ships config bs executes. Guards default from the workspace: `git_worktree` denies push to the default branch, force-push and merge; `none` and `command` deny push to the default branch when the dir is a git checkout, else nothing, and bs says so at spawn. `[guards] deny` adds; `replace = true` replaces. `bs config` prints every value and its source; `bs config init` writes the global file; `bs doctor` checks the harness, herdr client and server versions, socket modes, the logbook dir, each `cmd:` argv[0], a remote for `pr` goals, and stray `BS_*` variables.

**Refused at `bs spawn`** (exit 2, or 6 when gh couldn't be reached): a `pr`, `push` or `review` goal whose repo has no GitHub remote; a `push`/`review` target that isn't an open PR of the spawn's repo, or a fork without maintainer edits; a branch goal in a workspace with no branch; a custom type whose argv[0] isn't on PATH; a `command` kind with no `clean`; a pane whose herdr session has no config dir.

## Tests and the live rig

- **Fakes** (motif 13): a stateful fake herdr that encodes every probed constraint (control characters refused; a missing `--cwd` opens `$HOME`; `--source` before `--token` fails; tokens cut at 80; `pane move` renumbers and closes an emptied workspace; `agent_name_taken`; a start that times out or loses its terminal while the process runs; a pane not yet a shell; pane ids per server); `beesfake`, a real program the fake herdr starts as the pane's process, which runs scripted `bs` calls, listens on a ring socket and starts children, so liveness, identity, the gate and process groups run for real; real git in temp dirs; a fake `gh` and a fake `cl`.
- **Scenarios**: every check of the pilot's round-9 to round-11 sweeps, every pilot bug, every refusal above, and the goal rules, each with assertions on entries, output and exit codes.
- **Live rig**: one `herdr --session <toy>` server per toy, started with `env -i` plus `PATH` (the build under test first), `HOME`, `TERM`, `LANG`, `SHELL`; XDG left alone (a separate XDG home broke the keeper's shell and overflowed `sun_path`, probed). bs reads a pane's config from `~/.config/bees/sessions/<HERDR_SESSION>/` when the session isn't `default`; with that dir missing, every writing command refuses. Teardown stops the toy's server.

## Build plan

1. **Skeleton** (bees-13.2): a second command, `cmd/bsx`, on main over new packages, never built by `go install ./cmd/bs`, refusing the default config dir until the swap. It carries over the pilot's migration, pane scan, start confirmation (with its 12.49 adoption and 12.33 busy retry), teardown (guests, `stop()`), merge proofs, messaging rules, gate and their tests; it changes the identity key, end, prune, prompts, `bs done` and config as above.
2. **First feature live** (bees-13.3): claude_code + `git_worktree` + `--goal branch`, spawn to prune in the rig, no network.
3. **Then, one at a time:** `pr` with its states, `push`, `review`, `bs check`, `bs goal`, Rule G's refusals and guards; `verdict`, `note`, `beads`; mail across projects; `bs config init`, `bs doctor`; the swap, after every session the old `bs` started has been ended; `none`; `command` with a CL-shaped toy; omp (bees-12.8); agy (bees-12.12); a bakeoff replay.

## Considered options

- **Merge the pilot and refactor.** The keeper chose a rebuild on main instead; the pilot is reference.
- **Done kinds in config with `bs done --pr/--report`** (round 1), and **output types with evidence searched for** (round 2): replaced by goals fixed at spawn, after the review found the search could claim another worker's PR.
- **`done` waiting for the merge.** Kept apart: the handoff is the review signal; completion is the merge, checked by `bs end`.
- **`bs landed`, a `discarded` kind, a `--goal` default from labels or the workspace.** Dropped by Rules D and G: completion and discards are checked or amended, never typed, and the queen always names the goal.
- **A Workspace port with a package per VCS.** Rejected again: bs would grow VCS code it can't test; `command` argv does the job.
- **A separate XDG home per toy; `BS_CONFIG_DIR`.** The first breaks the keeper's shell; the second is an env pin back for one key.
- **Adopting the old `bs`'s sessions at the swap.** Rejected: end them first, as the pilot's rollout did.

## Consequences

- The logbook gains `queen`, `started`, `goal`, `done` and `landed` in one rebuild; the CHECK only grows.
- `bs spawn` without `--goal` is refused; the role prompts name the goal and stop guessing what "done" means.
- `bs end` no longer removes a worktree; prune does, on the frozen rule. Main's `bs end` exit codes keep their meaning; 7 (a handoff check failed), 8 (a start unconfirmed) and 9 (`bs end` on an incomplete goal) are new.
- Every rebuild PR is compared with the pilot's version of the same part: what it keeps, what it changes, which pilot bug it must not bring back.

## Amendment 1 (bees-13.6, keeper, 2026-10-22)

- **Review host and VCS ports.** The pr, push and review checks see a change only through `port.Review` (parse, view, CI, behind, the default branch's protection, fetch, the heads of merged changes, its merge-deny guard globs) and a workspace's commits only through `port.VCS` (commits since, branch tip, ancestry, a detached checkout). `review.host = "github"` (global or project, the only value yet) picks GitHub through gh and git, the default; a CL-shaped host plugs in there without touching the goal code. The scenario suite runs every PR-goal scenario on GitHub (fake gh over real git) and on `fake.CLHost`, whose checks use neither; its workspaces are still git worktrees until `command` (bees-13.11).
- **CI is bs's word.** Every PR goal's handoff (`pr`, `pr:ready`, `pr:draft`, and `push=` in each state) needs every CI check on the change's current head to pass (`ci_green`); drafts too, the keeper ruled on 2026-10-22. Checks still running, or none registered while the base's tip has some, with nothing else failing, is exit 10, pointing at `bs done --wait`; a failed check is exit 7, naming it. A vacuous pass is printed. #104–#107 each sent a done with CI pending, and sat red unnoticed.
- **`bs done --wait`** (alias `bs handoff`): on CI still running, bs asks the review host again every 20 s, in the worker's own foreground call (no background job of the harness's), for up to `--timeout` (8 min, under a harness shell call's 10), then hands off or names what failed; still running then is exit 11. The gate runs again after a wait.
