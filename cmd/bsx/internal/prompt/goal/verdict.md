`--goal verdict`: the work is an answer, yes or no, and a report saying why; complete once handed off.
<!-- worker -->

- Hand it off with `bs done --answer yes|no --report "<text>"`, or `--report -` to read a long report from stdin (`bs done --answer no --report - <<'EOF'`). The answer and report reach the queen in the done entry: never leave the report only in a file in your workspace or a scratch dir, which nobody reads and which goes when the workspace does.
- bs refuses an answer other than yes or no, or no report (exit 7), and `bs done "<text>"` (exit 2): say it with `--report`.
<!-- queen -->

- A `--goal verdict` worker's `bs done` reaches your inbox with its answer and report: relay both to the keeper. It is complete at once: `bs end <worker>`.
