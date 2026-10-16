# S3b mid-flight steer (slow worker)

S3 with a worker slow enough to steer: in S3 every arm's worker finished in about 12 s, before the steer.

1. t0: `write story.txt, a 12-line story about a bee. Write it one line at a time: append a line, commit, then sleep 20 seconds before the next (it's a steering test, keep the sleeps)`
2. Poll `git -C <repo> log --all --oneline -- story.txt | wc -l` every 5 s. When it reaches 2 → `change of plan for the story: stop at 6 lines total, and make all 6 rhyme AABB`. Log `event:` with the commit count at that moment.
3. Log `event:` when the steer reaches the worker: the first commit or worker message that shows it (the lead says it relayed it; the story stops growing past 6 lines; the rhyme shows up).
4. Lead reports ready → `merge it` → merged → `clean up` unless already done → `end`.

Optimal: the steer reaches the same running worker within one sleep (≤ 25 s), the story stops at 6 lines, rhyming AABB, one branch, no second worker. Keeper turns: 3.
