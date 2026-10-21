---
name: setup-bees
description: Walk a new user through installing and checking everything bees needs (Go, Claude Code, herdr, a shared local bd store, bs, pre-commit). Use when someone wants to set up, install or try bees/bs on a machine, or when bs fails because a prerequisite is missing.
---

# Set up bees

Follow [`docs/SETUP-AGENT.md`](../../../docs/SETUP-AGENT.md) step by step: it is the harness-neutral setup, and done means its last step's round trip ran. In Claude Code:

- The user runs interactive commands as `! <command>` lines, so the output lands in the conversation.
- Step 5 is the `claude_code` section; ask its permission-mode and bypass-mode questions in step 2's batch.

## Contributors to bees only: pre-commit

After step 6, if the user will change bees itself:

```sh
uv tool install pre-commit && pre-commit install
pre-commit run --all-files
```

`uv tool install` puts `pre-commit` on `PATH` (`~/.local/bin`), so worker worktrees run it too; the hook lands in the clone's shared `.git/hooks`. **Check:** `pre-commit run --all-files` passes in the bees repo.
