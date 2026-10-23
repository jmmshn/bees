# toy repo (CL-shaped)

A toy depot for scripted bake-off runs. Keep changes small and only touch what the task names.

- Work is never done in this checkout. Each piece of work gets a client of its own: `cl client new <name> <dir>` (run here) makes `<dir>`, a plain directory with a copy of this depot.
- In the client: edit, then `cl upload -m "<description>"` sends its edits as a CL named after the client; report the client's name. `cl diff <name>` shows a CL. There is no git in a client.
- Only the keeper decides when a CL lands. When the keeper says submit: `cl submit <name>` (run here), then `cl client delete <name>`.
- `cl` with no arguments lists its commands.
