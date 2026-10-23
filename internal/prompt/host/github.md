Review host: GitHub (`review.host = "github"`). A change is a pull request, its URL `https://github.com/<owner>/<repo>/pull/<n>`.
<!-- worker -->

- A pull request: push your branch (`git push -u origin <branch>`), open it with `gh pr create` (`--draft` for a draft), and push later commits to the same branch. Your branch holds at most one open PR, and you never cut a second branch: work that wants its own PR is another bead, so tell the queen, who stacks a worker on your PR.
- A review: `gh pr review <url> --comment --body "..."` or `gh pr comment <url> --body "..."`.
<!-- queen -->

- Merge a PR with `gh pr merge <url>` (run `{{bs}} check <worker>` first), once the keeper says to land it.
- Deleting a stacked PR's base branch closes the PR above it: retarget that one first (`gh pr edit <n> --base <default branch>`).
