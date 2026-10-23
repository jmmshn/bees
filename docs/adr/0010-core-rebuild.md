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
| `done` | `bs done` (the handoff) | the PRs handed off so far, or a verdict; every check's result *(Amendment 4)* |
| `landed` | `bs end`, `bs prune`, or a `done` | the complete checks that passed; `by: end \| prune \| done` |
| `ended` | `bs end` | `force`, `incomplete` |
| `message`, `received` | `bs send`, `bs inbox` | as today |

- **Liveness** is the recorded harness process alive with its recorded start time, for every harness (amends ADR-0004). herdr's agent status is never read.
- **Identity** is the pane: (`HERDR_SESSION`, or `default` when unset; `HERDR_PANE_ID`) → the open entry that recorded it, accepted only while its recorded process is alive and an ancestor of the calling bs. The session is part of the key because a `herdr --session` server numbers its panes from `w1:p1` (probed, bees-13.1), and every later herdr call for a spawn goes to `herdr --session <recorded>`. bs sets no `BS_*` in a session's env; inherited ones are ignored inside a recorded pane, with a warning. Inside one the project is the logbook whose entry recorded the pane, and the repo the one that entry recorded (bees-13.15, 12.43); elsewhere it is the git repo bs runs in, and `BS_PROJECT` names it when bs isn't in a repo and is refused (exit 2) when it names another (bees-12.47). `--project` is dropped except on `bs send`, which writes another project's logbook (T11). Outside a recorded pane (tests, scripts, the keeper's shell, a herdr pane no entry recorded), `--as` and the other `BS_*` variables apply as before (bees-13.14). *(R52: proposed by bees-13.3, narrowed to a recorded pane by bees-13.4, accepted by the keeper 2026-10-22.)* *(Amendment 4: identity is a recorded ancestor in the one project the call acts in)*
- **Start confirmation** polls `herdr pane process-info` for the harness process within 20 s and takes the baseline at first sight (amends ADR-0007). A start that confirmation missed is healed on any later trigger when the harness process in the recorded pane has `--name <worker>` in its argv; the `started` entry is marked `late`, and `bs status` shows it.
- **The background-work gate** is a built-in check, `no_background`, that every goal's handoff carries implicitly and `bs end` also runs: descendants not in the baseline, minus the caller's ancestors and process group (bees-12.50, 12.52), still alive a second later. Exit 5; can't check, exit 6. `--force` waives it and nothing else, except that on `bs end` it also waives the refusal of a worker whose pane moved out of its workspace, stopping its recorded process (bees-12.46, bees-13.5). A process that detaches escapes it: a speed bump, said once per spawn.
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
bs spawn --goal push=<pr-url>         fs-2.14     # also push:ready=, push:draft=   # Amendment 4: narrow; pr=<url> stacks
bs spawn --goal review=<pr-url>       dotfiles-6.6
bs spawn --goal verdict | note | beads | branch | branch:ready | <custom>
bs spawn --goal pr --goal note        bees-13.64  # refused (exit 2): one --goal per spawn   # Amendment 4
```

Grammar `<type>[:<state>][=<target>]`; the target is everything after the first `=`.

| type | the worker gives at `bs done` | handoff: bs checks | complete (`landed`) | prune rule |
|---|---|---|---|---|
| `pr` | `--pr <url>` (repeatable), `--replaces <url>` | each PR open, not a draft, holds a workspace commit, pushed, current with its own base, required CI green | every handed-off PR merged | merged *(Amendment 4)* |
| `pr:ready` | same | same | at handoff | every PR merged *(Amendment 4)* |
| `pr:draft` | same | each PR exists, holds a workspace commit, pushed; no CI check (keeper, 2026-10-22) | at handoff | every PR merged or closed *(Amendment 4)* |
| `push=<url>` | — | the target holds a workspace commit; current; required CI green (`push:draft=`: the first only) | target merged | as `pr` *(Amendment 4)* |
| `review=<url>` | — | a comment on the target since spawn carrying `bs:<spawn id>` | at handoff | at once *(Amendment 4)* |
| `verdict` | `--answer yes\|no --report <text>\|-` | the answer yes or no, the report not blank | at handoff | at once |
| `note` | — | the bead's notes or design differ from the hash taken at spawn | at handoff | at once |
| `beads` | — | a bead created since spawn under the bead | at handoff | at once |
| `branch` | — | the recorded branch ahead of base, clean | merged into base (the pilot's three proofs) | same |
| `legacy` | (spawns from before goals) | — | — | the pilot's three proofs |

- **The worker's only freedom** at exit is its PR URLs, or a verdict. bs checks each URL: its commit range must hold a **workspace commit**, one made in the recorded workspace since the first spawn that recorded the directory: written there (reflog entries that write a commit: commit, amend, a merge commit, revert; never pull, checkout, reset, a fast-forward, cherry-pick or a rebase's picks), or a rebased copy of one, matched by patch-id, so a rebase by any route keeps counting and work pulled in from other workers never does (keeper, 2026-10-22). A typo can't name someone else's PR. Handed-off PRs accumulate across `bs done` calls; `--replaces` drops one. *(Amendment 4)*
- **CI green**: every required check passes (`gh pr checks --required`); a check nothing requires never counts. The required names come from the base's protection (classic and rulesets, `RequiredChecks`): one not reported yet is pending, and the pass is vacuous, printed as one, only when the protection requires none. Protection that can't be read is exit 6.
- **The bead at spawn.** Every `spawned` also freezes its bead (`bead_at`: a hash of its notes and design, and the beads under it), whatever the goal, so a goal amended to `note` or `beads` checks against the spawn. Only a verdict takes `--answer`/`--report`; any other goal refuses them, naming where its words go (bees-13.7).
- **Frozen.** `spawned` holds the resolved goal: every check, each `cmd:` argv and timeout, the fragment's hash, and `prune.ignore`. `bs done`, `bs end` and prune read it (or the newest `goal` entry), never config.
- **Amend.** The queen or keeper may amend a live, incomplete goal (`bs goal <name> <value>`, `bs goal <name> --waive <check>`); only the keeper, from a shell with no recorded queen or worker among its ancestors, may set `discard`, at any time. Refused from a worker's descendants, after `landed`, and after `ended` except a discard. An amend re-runs the spawn-time refusals for the new goal.
- **`bs done`** stops at the first check that can't run (exit 6) and otherwise runs every handoff check and reports every failure at once (exit 7). `bs check <name>` runs the handoff checks now and writes nothing; the queen runs it before merging.
- **`bs end`** is the completion gate: complete → `landed`, then `ended`; not complete → exit 9, naming each PR and its state; `--incomplete` ends it anyway, handed off or not (the queen's abort; amended by bees-13.9: a refusal when nothing was handed off left the queen no way to cancel a worker) and prune completes it later, never removing unlanded work. It closes the herdr workspace and stops the process but keeps the directory.
- **`bs prune`** removes a workspace only when its frozen prune rule passes (or it is discarded) and it has no uncommitted, untracked or ignored files, naming what it kept (exit 3, or 4 when it couldn't check). Ignored files count because `git worktree remove` deletes them unseen, and an ignored `scratch/` is where five pilot reports were lost; only ignored files `prune.ignore` covers (build output, default `bin/`, as frozen at spawn) go with the worktree (keeper, bees-13.7). Closing the bead stays with the queen or keeper (motif 5).
- **Custom types** live in `[goal.<name>]` (`handoff`, `complete`, `prune`, `fragment`). A `cmd:` check is argv from config, never a shell; placeholders come only from the record and from checked handoff values, each filling one whole argument; a value starting with `-` is refused and a ref passes `git check-ref-format`; cwd is the recorded dir, env only `PATH`, `HOME`, `LANG`; a timeout kills the process group. Commands come only from the config files, never from bead text, labels or a worker.

The types come from 151 `done` messages in the five real logbooks: about 110 single PRs, eight beads with stacked or follow-up PRs, three replaced PRs, 15 "updated" re-sends, and the rest notes, verdicts, reviews and reports. "Blocked" is never a `done`: the worker messages the queen.

## Config

| key | file | decides |
|---|---|---|
| `harness` | global | which program starts in the pane |
| `review.host` | both | where pr, push and review goals' changes are reviewed and land: `github`, the default and only value yet (Amendment 1) |
| `paths.logbook_dir`, `paths.repos` | global | where logbooks live; where other projects' repos are |
| `paths.repo`, `paths.worktrees` | project / both | the project dir; `<worktrees>/<name>` |
| `workspace.kind` (override), `[workspace.command]` | both | tab above |
| `[goal.<name>]`, `[checks.<name>]` | both | custom types and their argv |
| `prune.ignore` | both | ignored files prune removes with a worktree (default `["bin/"]`), as `.gitignore` spells them: `dir/` covers a directory, a pattern with no other `/` matches at any depth, one with an inner `/` from the top; frozen at spawn; every other ignored file keeps it |
| `[guards] deny`, `allow_merge_into` | both | deny globs, added to the default; the merge grant: the worker is told its bases, the merge deny lifts for every base (a glob can't see a PR's base), so it is refused unless the default branch is protected |
| `[queen.<harness>]`, `[worker.<harness>]`, `worker.path` | both / project | the harness's own knobs; dirs first on a worker's PATH |

Both files live under the config dir, `~/.config/bees`; no repo ships config bs executes. Guards default from the workspace: `git_worktree` denies push to the default branch, force-push and merge; `none` and `command` deny push to the default branch when the dir is a git checkout, else nothing, and bs says so at spawn. `[guards] deny` adds; `replace = true` replaces. `bs config` prints every value and its source; `bs config init` writes the global file; `bs doctor` checks the harness, herdr client and server versions, socket modes, the logbook dir, each `cmd:` argv[0], a remote for `pr` goals, and stray `BS_*` variables.

**Refused at `bs spawn`** (exit 2, or 6 when the review host couldn't be asked: network, auth): a `pr`, `push` or `review` goal whose repo belongs to no repo on the review host (on GitHub: no GitHub remote); a `push`/`review` target that isn't an open change of the spawn's repo, or a fork without maintainer edits; a branch goal in a workspace with no branch; a custom type whose argv[0] isn't on PATH; a `command` kind with no `clean`; a pane whose herdr session has no config dir. *(Amendment 4)*

## Tests and the live rig

- **Fakes** (motif 13): a stateful fake herdr that encodes every probed constraint (control characters refused; a missing `--cwd` opens `$HOME`; `--source` and `--token` in either order (probed on 0.9.5; bs sends `--source` last); tokens cut at 80; `pane move` renumbers and closes an emptied workspace; `agent_name_taken`; a start that times out or loses its terminal while the process runs; a pane not yet a shell; pane ids per server); `beesfake`, a real program the fake herdr starts as the pane's process, which runs scripted `bs` calls, listens on a ring socket and starts children, so liveness, identity, the gate and process groups run for real; real git in temp dirs; a fake `gh` and a fake `cl`.
- **Scenarios**: every check of the pilot's round-9 to round-11 sweeps, every pilot bug, every refusal above, and the goal rules, each with assertions on entries, output and exit codes.
- **Live rig**: one `herdr --session <toy>` server per toy, started with `env -i` plus `PATH` (the build under test first), `HOME`, `TERM`, `LANG`, `SHELL`; XDG left alone (a separate XDG home broke the keeper's shell and overflowed `sun_path`, probed). bs reads a pane's config from `~/.config/bees/sessions/<HERDR_SESSION>/` when the session isn't `default`; with that dir missing, every command that opens a logbook refuses (the defaults would name the real one) and `bs config` and `bs doctor` run, warning (amended by bees-13.3, R52). Teardown stops the toy's server. *(Amendment 4)*

## Build plan

1. **Skeleton** (bees-13.2): a second command, `cmd/bsx`, on main over new packages, never built by `go install ./cmd/bs`, refusing the default config dir until the swap. It carries over the pilot's migration, pane scan, start confirmation (with its 12.49 adoption and 12.33 busy retry), teardown (guests, `stop()`), merge proofs, messaging rules, gate and their tests; it changes the identity key, end, prune, prompts, `bs done` and config as above.
2. **Config and the live rig** (bees-13.3), **sessions and the claude_code adapter** (bees-13.4), then the **first feature live** (bees-13.5): claude_code + `git_worktree` + `--goal branch`, spawn to prune in the rig, no network.
3. **Then, one at a time:** `pr` with its states, `push`, `review`, `bs check`, `bs goal`, Rule G's refusals and guards; `verdict`, `note`, `beads`; mail across projects; `bs config init`, `bs doctor`; the swap, after every session the old `bs` started has been ended; `none`; `command` with a CL-shaped toy; omp (bees-12.8); agy (bees-12.12); a bakeoff replay.

## Considered options

- **Merge the pilot and refactor.** The keeper chose a rebuild on main instead; the pilot is reference.
- **Done kinds in config with `bs done --pr/--report`** (round 1), and **output types with evidence searched for** (round 2): replaced by goals fixed at spawn, after the review found the search could claim another worker's PR.
- **`done` waiting for the merge.** Kept apart: the handoff is the review signal; completion is the merge, checked by `bs end`.
- **`bs landed`, a `discarded` kind, a `--goal` default from labels or the workspace.** Dropped by Rules D and G: completion and discards are checked or amended, never typed, and the queen always names the goal.
- **A Workspace port with a package per VCS.** Rejected again: bs would grow VCS code it can't test; `command` argv does the job. (Amendment 1's `port.VCS` is narrower: how the goal checks read a workspace's commits, and a push goal's starting checkout. herdr and `command` argv still make and remove workspaces.) *(Amendment 4)*
- **A separate XDG home per toy; `BS_CONFIG_DIR`.** The first breaks the keeper's shell; the second is an env pin back for one key.
- **Adopting the old `bs`'s sessions at the swap.** Rejected: end them first, as the pilot's rollout did.

## Consequences

- The logbook gains `queen`, `started`, `goal`, `done` and `landed` in one rebuild; the CHECK only grows.
- `bs spawn` without `--goal` is refused; the role prompts name the goal and stop guessing what "done" means.
- `bs end` no longer removes a worktree; prune does, on the frozen rule. Main's `bs end` exit codes keep their meaning; 7 (a handoff check failed), 8 (a start unconfirmed) and 9 (`bs end` on an incomplete goal) are new; Amendment 1 adds 10 (only pending left at a handoff) and 11 (`bs done --wait` ran out of time).
- Every rebuild PR is compared with the pilot's version of the same part: what it keeps, what it changes, which pilot bug it must not bring back.

## Amendment 1 (bees-13.6, keeper, 2026-10-22)

- **Review host and VCS ports.** The pr, push and review checks see a change only through `port.Review` (parse, view, CI, behind, the default branch's protection, fetch, the heads of merged changes, its merge and approve guard globs) and a workspace's commits only through `port.VCS` (commits written since, their patch-ids, branch tip, where a branch was last pushed, ancestry, and a push goal's starting checkout). `review.host = "github"` (global or project, the only value yet) picks GitHub through gh and git, the default; a CL-shaped host plugs in there without touching the goal code. The goal fragments name no host's commands: `prompt/host/<host>.md` says how a change is uploaded, reviewed and merged there. The scenario suite runs the PR-goal scenarios on GitHub (fake gh over real git) and on `fake.CLHost`, whose checks use neither; one is GitHub-only (GitHub lagging a push: a CL host has no local branch to push). The CL host's workspaces are still git worktrees until `command` (bees-13.11). The branch goal and prune's workspace removal stay git, as git_worktree's. *(Amendment 4)*
- **CI is bs's word.** The handoff of an open, ready change (`pr`, `pr:ready`, `push=`, `push:ready=`) needs every *required* CI check on its current head to pass (`ci_green`, `gh pr checks --required`): a failing or slow optional check never blocks it. Which checks are required is read from the base's protection, never from what the host has reported so far: right after a push it reports none, and a required check not reported yet is pending (Rule G). A pass is vacuous, and printed, only when the protection requires none. Drafts (`pr:draft`, `push:draft=`) get no CI check at all. (Keeper, 2026-10-22; this supersedes the same day's earlier ruling that drafts need green CI too, and the first cut's every-check rule and its "none registered while the base's tip has checks is pending".) Only *pending* left, with nothing else failing, is exit 10, pointing at `bs done --wait`. Pending is: required checks still running or not reported yet, or a push the host doesn't show yet (a commit made here, pushed, descending from the head the host still shows: it lags a push by seconds, seen live). A head on the host holding commits the branch lacks is a fail that says to pull, never pending. A failed check is exit 7, naming it. A vacuous pass is printed. #104–#107 each sent a done with CI pending, and sat red unnoticed. *(Amendment 4)*
- **`bs done --wait`**, and **`bs handoff`**, which is `bs done` waiting by default: while only pending is left, bs asks the review host again every 20 s, in the worker's own foreground call (no background job of the harness's), for up to `--timeout` (8 min by default, at most 9, inside the 10-min shell call the harness adapter gives a worker, Claude Code's own default being 2; the wait is counted from the call's start, and its last look gets 45 s more), then hands off or names what failed; still pending after waiting is exit 11, a look that couldn't run 6. The gate runs again after a wait. While the host still shows an older head than the workspace pushed, `pr_pushed`, `push_moved`, `pr_current` and `ci_green` on that change are pending, never failed on the old head. *(Amendment 4)*
- **Amends** send the worker its new goal's fragments (and a review's marker), so the recorded fragment hashes are what it reads. An amend never adds a push target: a push goal's workspace starts at its target's head. *(Amendment 4)*
- **`allow_merge_into`** lifts the host's merge deny for every base (a guard glob can't see a change's base), names its bases in the worker's brief, and is refused unless the host protects the default branch; no grant lifts the approve deny.

## Amendment 2 (bees-13.8, 2026-10-22)

- **T5 restated.** ADR-0009's T5 row says that with herdr down "`bs status` errors". Under this ADR liveness is the recorded process (Core state), so `bs status` reads the logbook, the process table and the disk, and asks herdr only to heal an unconfirmed start, so it works with herdr down: that heal that can't reach herdr reads `unknown`, never `no`. The rest of T5 stands: `bs spawn` refuses with nothing recorded, and `bs send` stores the message (it rings through the harness, not herdr).
- **`bs doctor` runs when the settings don't.** A broken file is its first FAIL and the machine checks run on the defaults; a refused project (bees-12.45) is a FAIL and they run on the global file; each check it then can't make is printed as `skip`. A workspace kind `bs spawn` refuses (until bees-13.10 and 13.11, anything but `git_worktree`) is a doctor FAIL.

## Amendment 3 (bees-13.9, the swap, keeper, 2026-10-22)

- **The swap.** `cmd/bsx` became `cmd/bs` over `internal/`; the old core is gone. bs runs on the default config dir and the real logbooks; a `herdr --session` toy still reads its own dir and refuses the real logbooks' dir. A logbook with an open spawn the old bs started (before goals, no `started`) is refused before it is opened, naming each; `bs end` and `bs status` warn, and `bs end` closes no herdr workspace of such a spawn, since it can't prove the id is still the worker's (herdr reuses ids after a restart).
- **R48 kept.** `logbook.Open` refuses a missing file (`ErrNoLogbook`); `logbook.Create`, called only by `bs queen` and `bs spawn`, makes one and says so. *(Amendment 4)*
- **Resume.** `bs spawn` on an open spawn whose recorded process died restarts the harness in its recorded pane, resuming its conversation once it has read its inbox, and records a `started` that names the dead one (`replaces`). A running worker, another `--goal`, or a pane herdr lost is refused with the next step. A dead recorded start counts as none for the heal: a harness with `--name <worker>` in the pane, started by hand, is recorded late as replacing it.
- **The abort.** `bs end --incomplete` ends a worker handed off or not; prune, which removes only landed or discarded work, is the guard. A re-spawn of a bead whose last spawn ended with its worktree and branch kept opens a herdr workspace on them.
- **Mail is scoped to the open spawn** at read time: a name ever spawned reads only what was stored after its latest spawn while it is open, so mail for an earlier spawn, or between spawns, never reaches a later one (migrated mail included). Amends ADR-0002's unread query.

## Amendment 4 (bees-13.66, the rebuild spec, keeper, 2026-10-22)

The keeper closed the stack built on Amendments 1–3 (#118, #119, #120, #122, #123) to redo it against one design, settled over plannotator rounds on 2026-10-22. Two findings drive it. The keeper's #119 feedback: two workers never share a branch, and dependent work stacks; bees-13.60 checked that rule against the code and the five real logbooks, and the keeper accepted every one of its eleven recommendations (Q1–Q11). And bees-13.49/13.50: a worker ended its sibling and spoofed `--as`, because identity trusted the pane and the flag. Much of the goal machinery above existed only to police what the work-shape rule forbids, and most of the identity rules only to guess who is calling. Lines above marked *(Amendment 4)* are amended here.

### Work shape

- **The rule.** One bead → one worker → one workspace on its own branch (named after the worker, R21′) → at most one *open* change from it at a time; on a CL host, the workspace's chain of CLs. A series of beads is many workers, never one (Q1). A change a bees worker owns that needs a fix is fixed by resuming its owner (`bs spawn <owner bead>`, Amendment 3), never by a second worker (Q3). Work that wants a change of its own is another bead.
- **Goals.** `pr[:ready|:draft][=<url>]`, `push=<url>`, `review=<url>`, `verdict`, `note`, `beads`, `branch[:ready]`, custom. One `--goal` per spawn: a second is refused at the CLI (exit 2). The frozen `{"parts":[…]}` JSON stays, so old entries decode (Q10).
- **`pr=<url>` stacks** on the target change. The worker's branch is cut at the target's head commit, and its base is the target's head ref, both recorded. Refused at spawn (exit 2) on a merged or closed target, or a fork's. Complete is merged *and* reachable from the project base, so a change merged into a dead branch never reads complete (Q5). A handoff whose target merged meanwhile fails, naming the retarget.
- **`push=<url>`, narrow** (bees-13.60's B′, Q2). The worker's own branch, cut at the target's head commit (recorded as its start), pushed onto the target's head ref. Only for a same-repo or maintainer-editable target. Refused at spawn while an open spawn owns the target (its recorded branch is the target's head ref), with `next: bs spawn <owner bead>`. Handoff: the target's head is my branch tip, ahead of the recorded start, current, required CI green. Complete: the target merged. It keeps fixups on a change bees doesn't own (the keeper's, an outside contributor's) without a second provenance model.
- **`review=<url>`.** The worker gives `bs done --answer approve|changes --report <text>|-`. bs posts the report on the target as a `--comment` review, deduped by its spawn id (bs embeds its own spawn id, so a retry after a lost response finds its review and posts nothing; motif 10), and records the review's URL. bs never approves or requests changes: workers run as the keeper's account (Amendment 1's approve deny), and GitHub refuses both on one's own PR. The worker may post inline comments itself; bs doesn't check them (Q9).
- **Finding the change.** The worker names nothing. `port.Review.Changes(ws, since) → []Change` returns the changes from the recorded workspace created since this spawn, non-fork, in the project's repo: on GitHub, `gh pr list --head <branch>` of every state, filtered by `isCrossRepository=false` and creation time; on a CL host, the workspace's chain. None → fail with `next: gh pr create`; one open → it; two open → refuse naming both (Rule G); none open → the newest (Q4). `Landed` becomes a filter over `Changes`, which fixes its fork bug. Scoping by spawn time keeps a re-spawn's old merged change from completing the new spawn (motif 2). A branch pushed under another name isn't found, and bs says so; a project branch-name template is a later key (Q8).
- **Handoff** for `pr`-like goals: the change's head is my branch tip, ahead of the start recorded at spawn (so a worker that resets onto another's branch hands off nothing of its own), plus open, not a draft, current and required CI as before. Host lag is "the host's head is an ancestor of my tip, pushed", pending as before.
- **Prune**: the three proofs on the recorded base, falling back to the project base once that base is gone (a stacked worker's target merged and its branch deleted). `bs end` and `bs prune` of a lower worker are refused while an open spawn stacks on its change, with `next: gh pr edit <n> --base <default branch>` (Q6): the retarget pitfall becomes a check.
- **Removed**: `bs done --pr` and `--replaces`; `pr_workspace_commit` (the reflog and patch-id walk); `push_moved`; `VCS.Commits`, `PatchIDs`, `Detach` and `Drop` (`port.VCS` keeps `Branch`, `Pushed`, `Has`, `Descends`); detached workspaces; mixed goals and their branch cut; review markers the worker plants. Every workspace is the worker's own branch.
- **Migration** (Q11): `bs end` and `bs check` read the PRs a `done` entry recorded (`prs`) for one release, so spawns handed off before this amendment end as they were handed off; then that path goes. Old `push=` and mixed entries still decode and prune on their frozen rule; no open spawn holds one (bees-13.60 V7).

### Identity

Every bs call acts in **one project**: the repo it runs in (its main checkout, as R52). Who the caller is there comes from that project's logbook alone:

```text
caller = nearest ancestor process an open entry of P's logbook recorded (a queen's process, an open spawn's start)
       | else: an unrecorded herdr pane inside a live spawn's recorded herdr workspace → that worker (the tripwire, with a warning)
       | else: an ancestor recorded by another project X's logbook → a session of X acting in P (Cross-project, below)
       | else: P's keeper
```

- **A session's project is fixed at spawn**: the logbook that recorded it. R52's "inside a recorded pane the project is the logbook whose entry recorded the pane" is dropped. A worker that runs bs from another project's repo acts in that project, as a session of its own.
- **`HERDR_PANE_ID`** (with `HERDR_SESSION`, or `default`) is a hint, never identity: it heals an unconfirmed start (Core state) and feeds the tripwire. Ancestry wins over it.
- **`--as X`** is a debug override, accepted only from the keeper's shell: a call with no recorded ancestor in any logbook bs can see. It acts as X, with X's permissions. Under a recorded session, `--as` naming anyone else is exit 2 on every command. `bs inbox --as X` peeks: it marks nothing read.
- **`BS_*`** stays ignored under a recorded session, with a warning (R52).
- **`bs status`'s first line** says who bs takes the caller for: `as <session> (<role> of <project>, spawned <time>) · logbook <path>`; for the keeper, `as keeper (keeper of <project>) · logbook <path>`.
- **Residual escapes**, read as the keeper: a process outside the worker's process tree with no hint, such as one a worker starts in a herdr pane outside its own workspace, a daemon it orphans (`setsid`, `nohup`, a double fork), or a job it hands to `systemd-run`, `at` or `cron`. Same-uid processes can't be fenced from each other: this guards against a careless worker, not a determined one, and the docs say so.

### Permissions

One table, checked once per command after identity:

| role | may run |
|---|---|
| keeper | `spawn`, `end`, `prune`, `goal` (amend, waive, discard), `check`, `send`, its inbox |
| queen | `spawn`, `end`, `prune`, `goal` (amend, waive; never discard), `check`, `send`, her inbox; never `done` |
| worker | `done` (its own), `check` (its own), `send` as itself, its inbox |

A refusal is exit 2 naming the caller, its role and who may (`bs end: <s> runs this as a worker; only the queen or the keeper ends a worker`). A spawn's owner is always `queen-<project>`, whoever ran it, so its worker hands off to her. Commands the table doesn't name are Open questions below.

### Cross-project

- A session of project X acting in project P ≠ X may only `bs send queen-P`. The message is written to P's logbook first (S2); if P's queen isn't running, bs starts her in a herdr pane on P's repo, under a twin lock (motif 7: two sends never start two queens), and rings her. A repo that isn't a bees project (no logbook, R48) is refused: a send never creates a project.
- Everything else in P is refused: exit 2, `you are <s> of <X>; message queen-P`.
- **Replies route queen to queen.** queen-P answers queen-X, never X's worker directly; queens relay to their own workers.
- **The keeper's shell is unrestricted** in every project.
- **Toys.** The session that created a toy is that toy's keeper: `setup.sh` (live-test) and `bakeoff/mkrun.sh` record `created_by` (the session's name and its spawn entry, so a later spawn of the same name isn't) in the toy's config, and a caller whose recorded ancestor matches it is the toy's keeper.

### Logbook creation (bees-13.59)

Parallel first spawns on a new project once printed `no such table: entries`. Writes are already serialized (`BEGIN IMMEDIATE`, `busy_timeout`, the re-check under the lock in `rebuild`), but `OpenReadOnly`, which reads other projects' logbooks, opens any file that exists, schema or not. The rule, both halves:

- **A logbook file exists only with its schema.** `Create` builds the schema in a temporary file beside the target, closes it, and links it into place without replacing (`link(2)`, or `renameat2(RENAME_NOREPLACE)`), so exactly one creator wins and prints R48's `new logbook` line; a loser removes its temporary file and opens the winner's.
- **A reader takes "no `entries` table" for empty**, never an error, for files made before this rule.

The identity ticket reproduces the race first (N parallel first spawns, plus a concurrent read-only scan; eng-diagnosing-bugs), then fixes it.

### Open questions

1. **"Any logbook bs can see"** for the `--as` and cross-project checks: the logbook dir of the config in effect, plus the default config's? A worker live-testing a toy runs bs under the toy's config, while its own spawn sits in the real dir. Recommended: both.
2. **Commands the permission table doesn't name**: `status`, `config`, `doctor`, `version`, `bead ls` (read-only), `bead new`, `epic` and `queen`. Recommended: read-only ones open to every role in its own project, and to a cross-project session too; `bead new` and `epic` for every role; `queen` for the keeper only.
3. **A worker's send from another project's repo**: is `bs send queen-X` (its own queen) refused there, as the strict rule reads? Recommended: a session may always send to its own project's queen, wherever it runs.
4. **`--project` on `bs send`**: kept as the one way to act in another project without changing directory? Recommended: yes, naming the project the call acts in.
5. **Starting P's queen from a send**: which herdr session, and with what label? Recommended: the keeper's default session, labelled `queen <project>`.
6. **The keeper's inbox**: no session reads it today (R13 is open). Recommended: `bs inbox` from the keeper's shell shows mail addressed to `keeper`.

### Built by

Rebuild steps 2–5 under bees-13: a design, then tickets for find-the-change (`--pr` goes), stacked `pr=` and narrow `push=` (no detached workspace), one goal per spawn, bs-posted reviews (it also rewrites `goal/review.md` and `host/github.md`'s review line), and identity with permissions, cross-project send and the logbook-creation fix.
