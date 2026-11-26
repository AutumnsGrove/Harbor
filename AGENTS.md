# Agents

This repository's review rule lives here, in [Pull request reviews](#pull-request-reviews). Everything else
about how to work in it is in the README, the workflows, and the code.

## Pull request reviews

`claude-review` reviews every pull request. It checks out the merge commit, installs Go and the web and
worker dependencies, and runs the code under full Bash, so it takes minutes rather than seconds. Its check
is the **`claude-review`** row of `gh pr checks`. The reviewer's own notes live in the "Reviewing a pull
request" section of `CLAUDE.md`, which the action restores from the base branch.

**Do not push while `claude-review` is reading.** The workflow cancels its own superseded run, so a push
cancels the review in flight, and a cancelled run has already read most of the pull request and spent most
of what it was going to spend. Pushing to be fast is the expensive choice, not the cheap one.

Read the `claude-review` row and act on the state it reports:

| `claude-review` on this head | What to do                                                                                                                                                                                                             |
| ---------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| pending                      | **Wait. Do not push.** A push cancels it and the wait starts over.                                                                                                                                                       |
| skipped                      | A draft, a fork, or a pull request that changes `.github/workflows/claude-code-review.yml`: the action refuses a workflow file that differs from `main`.                                                                  |
| completed                    | Read the pull request's comments. A green tick only means the job exited 0 — it also exits 0 when it skips — so the review is the comment, not the tick.                                                                 |
| failed                       | It did not post. Say so on the thread, then push.                                                                                                                                                                        |
| still pending, and it has no start time | The run is queued for a runner, not late. GitHub enforces the 40-minute job timeout itself, so a job that is *running* cannot sit past it — it ends cancelled or failed, which is the row above. A run with no start (`gh run view <id> --json status,startedAt`) is waiting and will start, so wait. |

A red check from CI is the one exception to all of the above: fix it and push.

`pi-review` is parked while `claude-review` is tuned; it has no check row until its workflow is restored.
