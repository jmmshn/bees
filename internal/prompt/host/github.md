Review host: GitHub (`review.host = "github"`). A change is a pull request, its URL `https://github.com/<owner>/<repo>/pull/<n>`.
<!-- worker -->

- A pull request: push your branch (`git push -u origin <branch>`), open it with `gh pr create` (`--draft` for a draft), and push later commits to the same branch. All your work goes on this one branch, in at most one open PR; work that wants a PR of its own is another bead, so tell the queen.
- A review: bs posts it from your `{{bs}} done`. An inline comment on a line: `gh api repos/<owner>/<repo>/pulls/<n>/comments -f body="..." -f commit_id=<head sha> -f path=<file> -F line=<n>`.
<!-- queen -->

- Merge a PR with `gh pr merge <url>` (run `{{bs}} check <worker>` first), once the keeper says to land it.
- A PR behind its base (`{{bs}} check` fails `pr_current`): bring it current yourself with `gh pr update-branch <url>`, then `{{bs}} check <worker>` again once CI reruns; bs accepts GitHub's merge of the base on top of the worker's branch, so the worker need not pull.
- Deleting a stacked PR's base branch closes the PR above it: retarget that one first (`gh pr edit <n> --base <default branch>`).
