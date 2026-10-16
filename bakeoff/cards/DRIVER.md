# Captain agent brief

What to tell an agent that plays the captain for one run: "Read `bakeoff/cards/DRIVER.md`, then play `<card>` against `<run dir>`."

1. Read [PROTOCOL.md](PROTOCOL.md) and the card. The run dir's `run.json` has the lead's `pane`, `repo` and `lead` name; `<bees>` is the bees checkout.
2. The lead may still be starting: `herdr agent wait <pane> --timeout 60000` before the first line.
3. Use only herdr commands, the `bakeoff/*.sh` helpers, read-only git polls, and the interventions the card names.
4. Watch in short loops (≤10 s): `herdr agent wait <pane> --timeout 10000`, then read. Treat herdr status `done` like `idle`. React to a trigger within ~5 s.
5. After every `herdr agent prompt`, confirm the line was submitted (a `❯ <line>` entry in `herdr pane read`): a pre-filled suggestion can swallow it. Log each captain line exactly once.
6. A choice menu in the lead: pick the matching option with `herdr pane send-keys`, or type the card's answer into its free-text option.
7. If your own permission check refuses something, log `captain[blocked]: <what>` and go on; never work around it.
8. Finish: run `bakeoff/collect.sh <run dir>`, then report its JSON and a 5–10 line narrative of what happened at each step, every surprise and workaround, and the timings the card asks for. The transcripts `collect.sh` copies are the full record; pane scrollback is about one screen.
