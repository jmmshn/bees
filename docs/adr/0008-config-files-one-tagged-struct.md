# Config: two TOML files, one tagged struct, per-file errors

> Amended by [ADR-0009](0009-core-and-adapters.md): a top-level, global-only `harness` key selects the harness for every queen and worker; only the active harness's tables are valid, and switching it while this project's sessions run on another is refused. `[workspace]`, `[delivery]` and `[guards]` join the layout.

bs reads its settings from `$XDG_CONFIG_HOME/bees`, else `~/.config/bees`, on every OS (never `os.UserConfigDir()`, which is `~/Library/Application Support` on macOS). A relative `XDG_CONFIG_HOME` is an error. There are two files, both optional:

- the global `config.toml`;
- `projects/<project>.toml`, named by the normalised project name (R27⁶).

**One tagged struct.** `internal/config.Config` is the only place a setting is declared. Its nested structs mirror the TOML tables (`s.Worker.ClaudeCode.Model` is `[worker.claude_code] model`). Each leaf's tags give its TOML key, scope (`global`, `project` or `both`), default, env var and allowed values (`enum`). Precedence is flag > env (the `BS_*` variables keep working) > project > global > default, so the reflective loader fills every leaf default → global → project → env (flags come with the keys that have one, in bees-11.3) and records each value's source in a parallel `map[string]Source` keyed by the dotted key. BurntSushi/toml parses; `MetaData.Undecoded()` finds unknown keys and `IsDefined` tells which keys a file set. The project name is resolved (flag > `BS_PROJECT` > git) before the project file is read, so no file can change which project you are in.

**v1 keys** (stage 1, bees-11.1). They are declared and shown by `bs config`, and wired in by bees-11.3 (paths) and bees-11.4 (models):

```toml
[paths]
logbook_dir = "~/.local/share/bees"  # both; env BS_LOGBOOK_DIR
repos       = ""                     # global only; env BS_REPOS
repo        = ""                     # project only: the project's main checkout
worktrees   = "{repo}-WT"            # both; the parent dir of worker worktrees

[queen.claude_code]
model = ""                           # empty: Claude's own setting

[worker.claude_code]
model = ""
```

**Models** (bees-11.4). A non-empty `model` is passed to Claude Code as `--model=<value>`, one argument so a value starting with `-` can't become a flag,: on `bs queen`'s exec line, and on every worker launch, a resume included. On the line, not in the worker's `--settings` file, because `--model` beats `ANTHROPIC_MODEL` and every settings file, so the key means what it says; it costs the 768-byte launch budget `len(model)+9` bytes (with the separating space), and spawn's launch check counts it. bs never checks the name; an unknown one is Claude's to report. Empty passes nothing, so Claude's own setting applies. `/model` in the session still wins, and a running session keeps the model it started with: a changed key takes effect at the next start. A `--repo` worker gets its spawner's project's model, like the rest of its env (R46).

**Layout rule.** `[paths]` and the role tables say nothing about any harness. A harness's own settings nest under the role (`[worker.claude_code]`). There is no `harness =` selector until a second harness exists; until then another harness's table is an unknown key.

**Errors are per file.** An unknown key, a wrong type, a value off its enum, or a key in the wrong file (`paths.repos` in a project file) is an error naming the file and the dotted key path. Only TOML syntax errors carry a line: `MetaData.Keys()` has no positions. A missing file or directory is fine; an unreadable one is an error (fail closed). Every unknown-key, scope and enum error in both files is reported at once; a syntax or type error stops that file's decode, so it is reported alone. When a project is known, a file in `projects/` that spells the project another way (`jobflow-ray.toml` for `jobflowray`) is an error naming the right file, since bs would never read it. Any error in a file fails every command that reads it. The one exception is a fixed list: `send`, `inbox`, `status`, `bead`, `epic` and `version` skip the files whenever `BS_LOGBOOK_DIR` is set, so a keeper editing `config.toml` mid-run can't silence a fleet whose workers carry the store in their env. `init`, `queen`, `spawn`, `end`, `prune` and `config` always read them.

**Values.** Paths expand `~` only, never `$VAR` or `~user`, and must then be absolute; the loader checks this for every key tagged `path`, in a file or an env var, naming where the bad value came from. An empty path is refused unless its default is empty (unset). `{repo}` is the only placeholder, valid only at the start of `worktrees`: the main checkout of the repo the worktree is cut from (a full path, so anywhere else it would make nonsense). `bs config` prints each value as written, then a comment line with what bs resolved from it (the logbook file). `BS_LOGBOOK` (a file) still overrides the logbook `logbook_dir` would give.

**The logbook** is `BS_LOGBOOK`, else `<logbook_dir>/<project>.db`. Every spawn pins `BS_LOGBOOK_DIR` (the directory of that file) into the worker's env and blanks `BS_LOGBOOK` there, so a worker writes the store its spawner resolved whatever herdr's env holds; a `BS_LOGBOOK` not named `<project>.db` can't be named by a directory, so it refuses the spawn before anything is made. When the logbook is missing and a value `logbook_dir` overrides (the default, or the global file's under a project file's) holds `<project>.db`, every command refuses, the ones that would start a new logbook too: a new store there would split the project's mail. Move the file, or set `logbook_dir` back. bs keeps no record of earlier values, so editing one file's `logbook_dir` from one directory to another is not caught (bees-11.12). `BS_LOGBOOK` names a file outright, so it is never read as a move. `bs config` says when `BS_LOGBOOK_DIR` is set, since the skip list is then in force.

**Repo lookup**, the one exception to "nearest wins", because `repo` and `repos` are different keys: `--repo` > the git repo you are in, when it is the project's > the project file's `paths.repo` > `BS_REPOS` > the global `paths.repos`. A repo from `--repo` or `paths.repo` that is a linked worktree is the base a worker branches from, never where its worktree lives (that stays under the main checkout's `-WT`). herdr enforces half of this: 0.9.5 refuses a linked worktree as `worktree create --cwd` ("New and open worktree actions start from the repo parent workspace"), so bs runs it from the main checkout with `--base <the base's HEAD>`. A `--repo` or `paths.repo` that is not a git checkout at all fails only the commands that need a repo (`queen`, `spawn`): messages need only the logbook. The spawned entry records the base (`base`: the main checkout, or that linked worktree) beside the worktree and the main checkout (`repo`, where the branch lives). A worker's worktree is `<paths.worktrees>/<worker>`, `{repo}` being that main checkout. An orphan, an unrecorded herdr workspace that refuses a first spawn, is one open on that path or on the worktree the worker's latest spawn recorded, since `paths.worktrees` may have moved since it ended. `bs config` shows the repo, the link that gave it, any base, the worktrees dir, and the cache dir.

**The cache dir** follows `XDG_CACHE_HOME` on every OS (Go ignores it on macOS), else `os.UserCacheDir()`. It has no key. Per-start dirs (`<name>-dir-*`) go in `bees/bs` under the user's runtime dir `/run/user/<uid>` when it is theirs (from the uid, not `XDG_RUNTIME_DIR`, which herdr's panes may lack), else in `bees-<uid>/bs` under the temp dir, not the cache dir: agy lists one beside the workspace, sorted, and runs every command in the first, so it must sort after the repos (bees-13.13.28). It has no key either; `bs config` shows both.

**`bs config [--project p]`** prints one line per setting, defaults included, as `key = "value"  # source`: `default`, `global <file>`, `project <file>` or `env <VAR>`, plus `; global file only` or `; project file only` for a scoped key. Outside any project it reads the global file only and says so.

## How this serves the north star

The epic's north star: a new user on another harness sets up bees with agentic help, and the config shows what runs where.

- **Which harness runs the queen and the workers.** The keys themselves say it (`queen.claude_code.model`), and `bs config` adds one fixed header line, `# the queen and every worker run on claude_code: bs's only harness, so no key selects one yet`. That is a fact about this build, not a setting, so it is a comment, not a key; the `harness =` selector comes with the second harness.
- **Something an agent can copy.** `bs config`'s output is valid TOML (dotted keys, the source as a comment), so it is the commented example: an agent copies a line into the file its header names. The header lists both files bs looks for and marks a missing one `(missing)`. No separate example file is kept, so the example can't drift from the struct.

## Amends

- **R25 and ADR-0006:** worktrees live under `paths.worktrees` (default `{repo}-WT`, the old place) instead of always at `<repo>-WT/<name>`; the repo lookup gains `paths.repo` and `paths.repos` as above. Under `--repo` the worktree's repo is the base, not the location.
- **R46:** a worker's env gains a second `BS_*` variable, `BS_LOGBOOK_DIR`, so every worker writes the store its spawner resolved (bees-11.3).
- **R23:** the logbook and repo lookup gain config.

## Considered Options

- **A flat struct with dotted-key field names, or a key registry beside the struct.** Rejected: two places to declare a setting. The nested struct is the TOML layout, and the tags carry the rest.
- **Per-key error blast radius** (a command fails only on an error in a key it reads; stage 1, rev 3). Rejected by the keeper for per-file errors with the fixed skip list: simpler to explain, and nothing silently runs half-configured.
- **A commented `config.example.toml` in the repo.** Rejected: it drifts from the struct; `bs config` prints the same thing from the struct.
- **A `paths.cache_dir` key.** Cut: `XDG_CACHE_HOME` already moves it.
- **An agent-command knob.** Cut: free-form args could pass `--name` or `--resume` and break identity. Supported choices become typed keys.

## Consequences

- Adding a setting is one struct field with its tags; `bs config` and validation pick it up. Settings are strings only until a key needs another type.
- `bs config` shows what the files say now, not what a running worker got: a start's effective config is not recorded in the logbook yet.
- BurntSushi/toml is a new dependency.
