`--goal review=<pr-url>`: the work is a review of an open PR, posted on it; complete once handed off.
<!-- worker -->

- Post your review on the PR (`gh pr review <url> --comment --body ...` or `gh pr comment <url> --body ...`) with the marker your brief gives (`bs:<id>`) in its body, then `bs done`: bs looks for that marker on the PR since your spawn. Never approve or merge it.
<!-- queen -->

- A `--goal review=<url>` worker's `bs done` means its review is on the PR, marked with its spawn id.
