# AGENTS.md (bees)

Terms (queen, worker, bead, project, logbook) are defined in `GLOSSARY.md`; decisions live in `docs/DECISIONS.md` and `docs/adr/`.

## Dogfood bs

bees is built with bees. Run this repo's own work through `bs`, and treat every rough edge you hit as a bug in `bs`.

- Track work with `bs bead ls` / `bs bead new --parent bees-N` / `bs epic`.
- Hand a bead to a worker with `bs spawn --goal <goal> --label "<words>" <bead>` (`--goal pr` for a PR); talk with `bs send` / `bs inbox`; check the fleet with `bs status`; after a merge, `bs end`, `bs prune`, `bd close`.
- When `bs`, herdr, Claude Code or git/gh fails, misleads, or needs a manual workaround: run the `capture-pitfall` skill (`.claude/skills/capture-pitfall/SKILL.md`) to file it with its evidence, then fix `bs` (or spawn a worker to). The workaround is evidence, not the fix.
- One shared `bs` on PATH, and only the queen (or keeper) installs it, from `main`: after a bs PR merges, or when bs warns it is stale, pull main in the main checkout and run `go install ./cmd/bs` there (the queen's one exception to "no builds"). A stale `bs` silently tests yesterday's code; a branch build makes everyone test unmerged code.
- Workers never `go install`: build into the worktree (`go build -o bin/bs ./cmd/bs`) and dogfood with `./bin/bs`, or `PATH=$PWD/bin:$PATH` so commands your session starts use it too. Sessions `bs spawn` starts don't inherit that PATH: they get their prompt from your build but run the shared bs, so test spawns in a throwaway project (the `live-test` skill) and say which build ran what.
- Lint runs on commit once `pre-commit install` is done (docs/SETUP.md); `pre-commit run --all-files` lints everything.
- Verify with `go vet ./... && go test ./...`, then exercise the change live through `bs` (real herdr, real Claude sessions) with the `live-test` skill (`.claude/skills/live-test/SKILL.md`) before calling it done. It runs the check in a throwaway project and tears it down; a check run in this repo's project files its beads and mail into the real one.
- Role prompts live in `internal/prompt/` (QUEEN.md, WORKER.md, PITFALLS.md, BEADS.md); bs ships them to every project, so they hold only what is true in any project, and bees-repo rules go here. Edit them there, never in a live session. A pitfall line cites its bug as `(bs bug bees-N.M)` so other projects don't read it as one of their beads.
- A bead touching bs's permission code (`internal/harness/claude` settings, `internal/guards`, autoMode, permissions) gets its edits denied as self-modification; a queen relay can't clear that. Warn the keeper when spawning one: they okay the edit in the worker's pane.
- A PR that fixes an issue shows the issue in its body as a short fenced queen ↔ worker chat transcript: who ran or said what (`queen-bees ▸ bs inbox`) and what came back, once before the fix (the wrong output marked `← the bug`) and once after.
- A change to the role prompts, spawn or messaging: replay the matching scripted cards in `bakeoff/` against the baseline arms ([docs/bakeoff.md](docs/bakeoff.md)), and append the round's row to `bakeoff/HISTORY.md`.

## Motifs to watch

An earlier tool in this space solved each of these classes, many of them more than once. When a change or a dogfood failure smells like one, name the class in the bead and fix the class, not the instance.

### State and identity

1. **Inferred state.** Status worked out from side channels (worktree on disk, pane list, scrollback). Record every outcome as a logbook entry; read state from the log.
2. **Partial reads as truth.** A time window, tail or cursor that drops a slow or late entry; an old entry satisfying a check on something that recurs. Read the full log; check current state before history.
3. **One identity, many spellings.** Names rewritten for another system's charset, then reversed back to an id. One pure forward mapping; never reverse a lossy name.
4. **Routing by a non-unique key.** Replies matched by a correlation id where "first row wins". Route by session; ids are an echo check.

### Lifecycle

5. **Dead-end states.** A stuck session with no exit, an id that can't be reused, a record that can't be amended. Every live state has an abort; "worker says done" stays separate from "keeper closes".
6. **Side effects moving state.** A heartbeat or progress event undoing a delivery. Liveness touches liveness only.
7. **Twins and zombies.** A failed probe read as "nothing running" spawns a second copy; a superseded spawn attaches late. Fail closed on probe errors; fence each spawn.
8. **Half-built spawns.** A launch that fails after recording leaves a record, a worktree, or a resume path for a conversation that never started. Make spawn all-or-nothing, or make a second spawn heal it.

### Liveness and delivery

9. **False death, false life.** Long silent steps read as dead; attach-then-die reads as alive forever. Derive liveness from the live process.
10. **Fragile wakes.** Keystroke injection, lost Enter, double delivery, a wake that doesn't say where to read. The message is durable in the logbook first; a doorbell only changes *when* it is read, never *whether*.
11. **Silent edges.** "Empty answer" indistinguishable from "pending"; a timeout reported as failure when the thing actually started. Every outcome gets its own distinct, truthful output and exit code.

### Environment and blast radius

12. **Teardown beyond ownership.** Removing a workspace guessed from herdr. Tear down only what the spawn entry recorded.
13. **Fakes that disagree with the real tool.** A test double accepts arguments or states the real CLI refuses (herdr rejects newlines in agent args; the fake didn't). Encode each real constraint in the fake and probe the real tool when adding one.
14. **Wrong store, wrong environment, wrong binary.** A typo'd path silently creating an empty logbook; a trust prompt eating the launch; merged source never installed. Warn loudly on mutating an empty store; check the deployed binary.
15. **Runaway children and locks.** A timed-out child whose grandchild holds the pipe; a stalled writer freezing every process. Kill the process group; bound every lock wait.
