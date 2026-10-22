Review host: GitHub (`review.host = "github"`). A change is a pull request, its URL `https://github.com/<owner>/<repo>/pull/<n>`.
<!-- worker -->

- A pull request: push your branch (`git push -u origin <branch>`), open it with `gh pr create` (`--draft` for a draft), and push later commits to the same branch. A stacked PR: a new branch on top of yours, `gh pr create --base <your lower branch>`.
- A review: `gh pr review <url> --comment --body "..."` or `gh pr comment <url> --body "..."`.
<!-- queen -->

- Merge a PR with `gh pr merge <url>` (run `bs check <worker>` first), once the keeper says to land it.
