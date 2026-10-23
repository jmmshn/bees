---
name: capture-pitfall
description: File a rough edge as a bead with its evidence. Use when bs, herdr, Claude Code, git/gh or the queen/worker workflow fails, misleads, stalls silently, or needs a manual workaround.
---

# Capture a pitfall

The workaround is evidence, not the fix. Done means the evidence sits on one bead and the person above you knows its id.

1. **Capture** while it is fresh: the exact command, its full output and exit code, the time, `bs version`, `herdr --version`, `claude --version`, and the logbook entries or ids involved (`bs status`, `bs inbox`).
2. **Dedupe**: search `bs bead ls`, `bs bead ls --all`, and the Known pitfalls sections of `internal/prompt/QUEEN.md`, `WORKER.md`, `PITFALLS.md` and `harness/*.md`.
3. **Classify** against the motifs in `AGENTS.md`: name the class (e.g. "motif 11, silent edges") so the fix targets the class, not this instance.
4. **File**: a new rough edge goes in `bs bead new --parent <epic> --type bug --description "<evidence, class, suspected fix>" "<symptom as seen>"`; a known one gets the new evidence with `bd update <bead> --notes "..."`.
5. **Warn**, if it will bite again before the fix lands: add or adjust one Known pitfalls line, `what you see → what it means → what to do (<bead>)`, in the role file of whoever hits it (`PITFALLS.md` for both; `harness/<kind>.md` for a line true of only one harness, such as `claude.md`). Delete the line when its bead closes.
6. **Report**: a worker sends `bs send queen-<project> "pitfall: <bead> <one line>"`; the queen tells the keeper.
