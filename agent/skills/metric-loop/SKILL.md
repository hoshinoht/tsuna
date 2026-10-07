---
name: metric-loop
description: Run a bounded, git-backed experiment loop that makes one focused change at a time, measures it with a mechanical metric command, and keeps or reverts it. Use when the user wants to iteratively optimise a measurable number (runtime, loss, accuracy, bundle size, warning count, word count) inside an isolated worktree branch. Not for subjective or LLM-judged goals.
license: GPL-3.0-or-later
compatibility: Requires git (worktree support) and a shell. Platform-agnostic; any agent that can edit files and run commands can follow it.
metadata:
  domain: experimentation
  workflow: metric-loop
  inspired-by: https://github.com/uditgoenka/autoresearch (MIT)
---

# Metric loop

Improve one number through small, reversible experiments. Every experiment is a
commit on an isolated branch; every measurement is logged. The user decides
what, if anything, gets merged.

## Inputs

Collect these before starting. If a required one is missing or ambiguous and
you can talk to the user directly, ask once (batched), then proceed. If no
interactive ask tool is available, do not start: return BLOCKED listing the
exact missing or ambiguous fields.

| Input | Required | Notes |
|---|---|---|
| Goal | yes | one sentence: what should improve and why |
| Scope | yes | files or globs the loop may modify; everything else is read-only |
| Metric command | yes | shell command whose output contains the number |
| Direction | yes | `higher` or `lower` is better |
| Parse rule | yes | how to extract the number: last line, a regex, a JSON path, or exit code |
| Guard command | no | must exit 0 for a change to be kept (tests, build, lint) |
| Iterations | no | default 10; this is a hard maximum unless the user explicitly raises it |
| Time budget | no | per-run timeout for the metric and guard commands |
| Plateau | no | stop after N consecutive non-improving iterations; default 3 |
| Tag | no | short slug for the branch; default derived from the goal |

## Hard rules

1. **Mechanical metrics only.** The metric must come from a deterministic
   command (a timer, a test count, a compiler, a linter, a script that computes
   a score from data). Never use a score produced by an LLM, including yourself,
   as the metric or the guard. If the user's goal has no mechanical measure,
   stop and say so.
2. **Isolation.** Work only in a dedicated git worktree on branch
   `autoresearch/<tag>`. Never edit, stage, commit, reset or check out anything
   in the user's main worktree.
3. **Commit scope.** Starting a loop authorizes commits on the
   `autoresearch/<tag>` branch only. Never push, merge, rebase onto, or switch
   to `main` or any other branch. Merging is the user's call.
4. **Stay in scope.** Modify only files in Scope. Never edit the metric command,
   the guard, the evaluation data, or the test files to move the number.
   Improving the measurement instead of the system is a failed experiment.
5. **Budgets are hard.** Stop at the iteration budget. Kill a run that exceeds
   the time budget and record it as `timeout`.

## Setup

1. Confirm the repository root (`git rev-parse --show-toplevel`) and note the
   base commit (`git rev-parse HEAD`). If the main worktree has uncommitted
   changes inside Scope, warn: the worktree starts from the last commit and will
   not include them.
2. Create the worktree next to the repository, not inside it:

   ```sh
   git worktree add -b autoresearch/<tag> ../<repo>-autoresearch-<tag> HEAD
   ```

   If the branch already exists, do not reuse or overwrite it on your own. Ask
   the user whether to resume it or pick a new tag; without an interactive ask tool,
   return BLOCKED naming the existing branch and offering both choices.
3. `cd` into the worktree for every remaining step. Dependencies or build
   artifacts that live outside git may need the project's normal setup command;
   run it only if it installs nothing system-wide.
4. Create `results.tsv` in the worktree root with this header. Keep it
   untracked (never `git add -A` or `git add .`) so commits and reverts never
   touch it:

   ```
   iteration	timestamp	commit	metric	delta	guard	status	description
   ```

## Baseline (iteration 0)

Run the metric command (and the guard, if set) on the unmodified branch.
Record row 0 with `status=baseline`, `delta=0`. If the metric cannot be parsed
or the guard fails on the baseline, stop and report: the loop cannot judge
changes against a broken reference.

## Iterate

Repeat until a stop condition fires:

1. **Review.** Read the tail of `results.tsv` and `git log --oneline -20`.
   For the last kept change, read `git show --stat HEAD`. Note what helped,
   what regressed, and which ideas are untried.
2. **Change.** Make exactly one focused, explainable change inside Scope.
   Prefer ideas unlike recent failures. Do not bundle unrelated tweaks.
3. **Commit.** `git commit -am "experiment: <what and why>"` (add new files
   explicitly). Record the short SHA.
4. **Measure.** Run the metric command within the time budget and parse the
   number. Then run the guard, if set.
5. **Decide.**

   | Outcome | Status | Action |
   |---|---|---|
   | better than the best so far, guard passes | `keep` | leave the commit |
   | equal or worse | `discard` | `git revert --no-edit HEAD` |
   | guard fails | `guard-fail` | `git revert --no-edit HEAD` |
   | metric command errors, no number, or timeout | `crash` / `timeout` | `git revert --no-edit HEAD` |

   Compare against the best kept value, not just the previous row. Use
   `git revert`, never `git reset --hard`, so the history stays inspectable.
6. **Log.** Append one TSV row: iteration, ISO-8601 timestamp, SHA, metric (or
   `NA`), delta versus best, guard (`pass`/`fail`/`-`), status, description.

## Stop conditions

Stop at the first of:

- the iteration budget is spent;
- `Plateau` consecutive iterations without a `keep`;
- 3 consecutive `guard-fail` or `crash` rows (the approach is broken; report
  instead of thrashing);
- the user's target value, if one was given, is reached.

## Final report

Return:

- goal, metric command, direction, parse rule, budgets used;
- baseline value, best value, absolute and relative improvement;
- the kept commits (`git log --oneline <base>..autoresearch/<tag>` minus
  reverted pairs) with a one-line reason each;
- the worktree path, branch name and `results.tsv` path;
- how to inspect and adopt the result, leaving the decision to the user:

  ```sh
  git -C <worktree> log --oneline <base>..HEAD
  git diff <base>..autoresearch/<tag> -- <scope>
  # to adopt, from the main worktree, when you choose:
  #   git merge --squash autoresearch/<tag>   or   git cherry-pick <sha>...
  # to discard:
  #   git worktree remove <worktree> && git branch -D autoresearch/<tag>
  ```

- caveats: noise level, anything that looked like overfitting, and ideas left
  untried.

## Academic use

The loop fits ML, numerical and paper-hygiene work, with extra discipline.

**ML and numerical experiments**

- Fix every seed (framework, NumPy, Python, data loader) and log it. A result
  that changes with the seed is noise until shown otherwise.
- When run-to-run variance is comparable to the expected gain, repeat each
  measurement k times (k >= 3, ideally 5) with different fixed seeds and use
  the mean as the metric. Log mean ± std in the description column and only
  `keep` when the improvement exceeds the noise (for example, the mean gain is
  larger than one standard deviation).
- Record the full configuration of each iteration: hyperparameters, data
  version or hash, code SHA, and hardware if it affects timing. Commit the
  config file with the change, or write it into the description.
- Optimise against a validation split only. Never read, tune on or report
  intermediate numbers from the test split during the loop; evaluate on test
  once, at the end, with the chosen configuration, and say so in the report.
- Keep the evaluation script outside Scope so the loop cannot change how it is
  graded.

**Paper hygiene**

Mechanical metrics that work well on a LaTeX project:

| Goal | Metric command (example) | Direction |
|---|---|---|
| fewer lint warnings | `chktex -q main.tex sections/*.tex \| grep -c Warning` | lower |
| fit a word limit | `texcount -inc -sum -1 main.tex` | lower, with a target |
| build stays green | `latexmk -pdf -interaction=nonstopmode -halt-on-error main.tex` as guard | exit 0 |
| no undefined refs | `grep -c 'undefined' main.log` after a build | lower |

Do not let the loop trim content to hit a word count without the user having
approved which sections may be cut; list proposed cuts in the report instead.
Never introduce, alter or invent numerical results, citations or claims in a
paper to move a metric.
