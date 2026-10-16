# Keeper protocol (every card)

You play the keeper, the human, for one run. The run dir holds `run.json` (`repo`, `pane` = the lead's herdr pane, `lead`, `arm`). Talk to the lead **only** through its pane, exactly as a human would at the keyboard.

## Tools

- Say a line: `herdr agent prompt <pane> "<line>"`, then `herdr agent wait <pane> --until working --timeout 20000` (a bare wait straight after a prompt can return at once on the previous turn's idle/done), then `herdr agent wait <pane> --timeout 600000` (returns on idle/done/blocked). A lead woken by a worker's message goes back to working on its own: re-check with observe.sh before treating idle as final.
- Claude Code may pre-fill a suggested next prompt in the lead's input box (e.g. `check bs inbox`); it is not submitted and your next line replaces it. Ignore it.
- Read the lead: `herdr pane read <pane> --source recent --lines 80`.
- Watch the run: `bakeoff/observe.sh <run dir>` (lead status, worker panes, branches). In arm A workers are subagents inside the lead, so they don't show as panes; the lead's footer (`← N agents`) does.
- Watch a worker pane (only to log what it is doing): `herdr pane read <worker pane> --lines 40`.

## Log

Append one line per event to `<run dir>/keeper.log`, with the UTC timestamp `date -u +%FT%T.%3NZ` first:

```text
<ts> keeper: <exact line you typed>          a card line
<ts> keeper[offcard]: <line>                 anything you had to say that the card doesn't script
<ts> keeper[intervention]: <what you did>    a manual workaround: a `!` command, answering a worker's pane, a key press
<ts> event: <what you saw>                    "lead delegated (2 workers)", "lead: ready for review", "question surfaced: …", "worker pane blocked on permission prompt"
<ts> end                                      the card is finished (or "end (timeout)", "end (stalled)")
```

## Rules

1. Say the card's lines and nothing else; the card lists its triggers. If the lead asks you something the card doesn't answer, reply `your call` and log it as `keeper[offcard]`.
2. **Merges.** When the lead reports work ready, say the card's merge line. If the lead hands you a `!` command for a merge or push (its permission check blocked it), type that exact `!` line into the lead's pane (an intervention), then say `merged` (keeper line). If the `!` command fails, say `that failed` (keeper line) and let the lead handle it.
3. **Stalls.** If the lead is idle with work outstanding and no question for you for 5 minutes, say `status?` (keeper line). After 3 more minutes, log `event: stalled` and look at the worker panes: if one is stuck on a permission prompt or a question, answer it as the keeper would (intervention), log it, and continue. Do this at most twice, then `end (stalled)`.
4. Never edit the toy repo or run git in it yourself, except `!` lines the lead asked for.
5. Hard cap: 25 minutes from the first line, then `end (timeout)`.
6. When the card ends: save `herdr pane read <pane> --source recent --lines 400` to `<run dir>/lead.txt` (transcripts are copied by collect.sh; no worker pane captures needed). Log the moment the lead tells you work is ready as `event: lead reports ready`. Then run `bakeoff/collect.sh <run dir>` and report its JSON plus a 5–10 line narrative: what happened at each step, every surprise, every workaround, in the order it happened.

Leave cleanup to the lead; the card says when to ask for it. Don't close workspaces or kill processes yourself, except where a card says so.
