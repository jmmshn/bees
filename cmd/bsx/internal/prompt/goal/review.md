`--goal review=<url>`: the work is a review of an open change, posted on it; complete once handed off.
<!-- worker -->

- Post your review on the change (the review host's section says how) with the marker your brief gives (`bs:<id>`) in its body, then `bs done`: bs looks for that marker on the change since your spawn. Never approve or merge it.
<!-- queen -->

- A `--goal review=<url>` worker's `bs done` means its review is on the change, marked with its spawn id.
