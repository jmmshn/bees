## Beads

The bead tracker is shared by every project. Stay inside this one by going through {{bs}}:

- List: `{{bs}} bead ls` (beads that aren't closed). `{{bs}} bead ls --all` includes closed ones; extra arguments are ANDed as a `bd query` expression, e.g. `{{bs}} bead ls 'priority<=1'`.
- Search: `{{bs}} bead search [--all] <text>` finds the text in titles, descriptions, notes and design, ignoring case; `bd search` reads titles only.
- Add an epic: `{{bs}} epic "<title>"` → `{{project}}-N`.
- Add a bead under it: `{{bs}} bead new --parent {{project}}-N "<title>"`. Optional flags: `--description`, `--type`, `--priority`.
- Read or amend one of this project's beads with `bd show` and `bd update`.

`bd list` and `bd create` see and write every project, so reach beads through {{bs}}.
