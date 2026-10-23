Review host: CLs, through the `cl` tool (`review.host = "command"`). A worker's directory is its own client of the depot (the project's repo), named after the worker; its edits go out as one CL, its URL `https://cl.example/<repo>/<n>`.
<!-- worker -->

- A CL: edit files in your client, then run `cl upload -m "<one-line description>"` in it; it prints the CL's URL. Edit and `cl upload` again to revise the same CL. `cl diff` shows what you uploaded. A client has no branches and no git: bs reads your CL from the client itself.
- Never submit your CL: landing it is the queen's.
<!-- queen -->

- Read a worker's CL with `cl diff <worker>`; `cl list` shows every client and its CL's state.
- Submit a CL with `cl submit <worker>` (in the project's repo), once the keeper says to land it: it commits the CL's edits to the repo's main, and the CL reads as merged.
