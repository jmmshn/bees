`--goal review=<url>`: the work is a review of an open change, which bs posts on it; complete once handed off.
<!-- worker -->

- Read the change, then hand your review off with `{{bs}} done --answer approve|changes --report -` (the report on stdin: `{{bs}} done --answer changes --report - <<'EOF'`). bs posts the report on the change as a comment review, with your answer and a `bs:<spawn id>` marker, and records its URL; a retry finds that review and posts nothing more.
- Inline comments on particular lines you may post yourself (the review host's section says how); bs doesn't check them. Never approve, request changes or merge: bs refuses an answer other than approve or changes, or no report (exit 7).
<!-- queen -->

- A `--goal review=<url>` worker's `{{bs}} done` reaches your inbox with its answer, report and the review's URL: bs posted the review on the change. It is complete at once: `{{bs}} end <worker>`.
