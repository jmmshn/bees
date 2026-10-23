Review host: CLs, through the `cl` tool. A worker's directory is its own client of the depot (the project's repo), named after the worker; its edits go out as one CL, also named after it.
<!-- worker -->

- A CL: edit files in your client, then run `cl upload -m "<one-line description>"` in it. Edit and `cl upload` again to revise it. `cl diff` shows what you uploaded. There are no branches and no commits in a client: don't run git there.
<!-- queen -->

- Read a CL with `cl diff <worker>`; `cl list` shows every client and its CL's state.
- Submit a CL with `cl submit <worker>` (run it in the project's repo), once the keeper says to land it: it commits the CL's edits to the repo's main.
