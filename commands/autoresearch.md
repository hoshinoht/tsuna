---
description: Run a bounded metric-driven experiment loop on an isolated
  autoresearch/<tag> branch
agent: experimenter
---

Run a metric loop for this request using the `metric-loop` skill:

$ARGUMENTS

Expected fields (free-form or `Key: value`): Goal, Scope, Metric (command), Direction (higher|lower), Parse (how to read the number), Guard (optional), Iterations (default 10), Timeout per run (optional), Plateau (default 3), Tag (optional).

This request authorizes commits only on `autoresearch/<tag>` inside its own worktree. It does not authorize pushing, merging, or touching the main worktree. If a required field is missing and cannot be inferred unambiguously, return BLOCKED listing the missing fields. Report baseline, best, kept commits, the `results.tsv` path, and how to inspect or adopt the branch.
