`--goal cl`: the work is one CL, uploaded from the worker's client, complete once it is submitted.
<!-- worker -->

- Upload your work as your client's CL (the review host's section says how), then hand it off: `{{bs}} done`. It checks `cl sent <your name>` and refuses (exit 7) until your CL is out. Upload again after any later edit, then `{{bs}} done` again.
- Never submit your CL yourself: landing it is the queen's.
<!-- queen -->

- A `--goal cl` worker's `{{bs}} done` means its CL is out for review: read it with `cl diff <worker>`. Submit it when the keeper says so (`cl submit <worker>`), then `{{bs}} end <worker>`: it refuses (exit 9) until `cl submitted <worker>` holds. `{{bs}} prune` then deletes the worker's client.
