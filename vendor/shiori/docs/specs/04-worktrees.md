# 04 — Worktree-aware execution

Status: proposed later-stage extension. No implementation lanes were created
for this specification; earlier runtime tests used disposable scratch worktrees.

## 1. When isolation is useful

Use worktree lanes for overlapping edits, risky refactors, experiments or
dependency changes. Small disjoint slices may share a checkout. More lanes are
not automatically faster: setup, review, merge conflicts and integration tests
have costs. Parallelism remains subject to explicit ownership/dependencies.

Keep one canonical coordination root and one parent writer. Worker worktree
copies must not create competing `.opencode/workplan` state or mutate the parent
plan. Source checkout, coordination root and actual runtime session location
are distinct identities, each recorded and verified.

## 2. Versioned lane manifest

Candidate `<plan-id>.lanes.json` sidecar v1; parent-owned, not a mandatory V2
plan-field migration. Required lane facts:

| Fact | Purpose |
| --- | --- |
| Lane/work-package ID and owner/session | Stable claim and provenance |
| Canonical source repo and coordination root | Prevent cross-project writes |
| Actual checkout path and runtime location | Verify physical isolation |
| Base revision and baseline transfer fingerprint | Reproduce starting state |
| Branch/detached state, owned/blocked files | Explain integration and boundaries |
| Dependencies and lifecycle state | Prevent premature work/integration |
| Validation/diff fingerprints and receipt pointers | Bind evidence to code |
| Integration/cleanup disposition | Preserve abandoned or dirty work |

States should distinguish proposed, prepared, running, validated, integration-ready,
integrated, blocked and cleanup-required. A manifest is not authority to run Git,
commit, push, delete directories or reassign a live worker.

## 3. Baseline and workspace binding

Creating a worktree from `HEAD` omits uncommitted tracked and untracked changes.
Never pretend it reproduces the parent's dirty checkout. The parent must select:

- A clean pinned revision, with relevant dependencies explicitly integrated; or
- A reviewed patch/file manifest transferring relevant dirty state, including
  new files, with a reproducible fingerprint; or
- An explicitly authorized checkpoint commit. Commit permission is not implied.

Do not copy credentials, local databases, private environment files or arbitrary
untracked content. Dependency/cache sharing is a separate policy: worktrees do
not isolate ports, databases, node_modules, credentials or global plugins.

Before first source write, verify actual session location, filesystem checkout,
Git common directory/base revision and command cwd. A handoff saying “cwd=lane”
does not prove the native session moved. If the host cannot establish the binding
through supported APIs, block write dispatch rather than invent tool parameters.

Worker operations address lane source files; parent workplan writes remain in
the verified coordination root. Any cross-root access needs exact authorization
and separate trusted context, not model-supplied root overrides.

## 4. Lifecycle and integration

1. Parent proposes lane/ownership/dependencies and obtains required action approval.
2. Create through the supported Git/host worktree mechanism; never force an
   existing path or assume failed setup succeeded.
3. Verify binding and setup results, then claim the worker before write dispatch.
4. Collect lane tests and exact code/diff fingerprints; no worker edits shared plan.
5. Review incoming changes, detect ownership/conflicts and integrate centrally.
6. Re-run relevant checks on the combined state. Lane PASS is not combined PASS.
7. Remove only known owned, clean, safely integrated lanes; preserve dirty/unmerged
   work with cleanup-required diagnostics and request explicit disposition.

When commits are not authorized, use reviewed patch-based handoffs, not implicit
worker commits/merges. Reassignment requires positive evidence the old worker is
inactive and the claim released; timeout alone is not proof.

Pass Git arguments without shell interpolation. Branches/paths/options from
untrusted plan content must be validated. Never run setup/action prose implicitly.

## 5. OpenCode integration

V2 provides a bundled Git worktree strategy and public create/list/remove/refresh
interfaces. Strategy ownership and canonical-checkout configuration must be
verified against the actual supported runtime; an arbitrary plugin registration
can change the selected strategy. Creating a worktree may run setup scripts, so
creation authorization must include reviewed setup effects.

Prefer existing host lifecycle support rather than building a competing Git
inventory. Parent authority, baseline transfer and session binding are still
Shiori responsibilities. Remote workspace provisioning is not assumed to be part
of the local worktree interface.

## 6. Acceptance cases

Dirty/untracked baselines, overlapping ownership, wrong/nested session location,
setup failure, missing strategy owner, simultaneous lane claims, cancelled workers,
unmerged/dirty cleanup, patch/merge conflicts, post-integration failures and lost
processes must all have explicit outcomes with no discarded user work.

- [V2 worktree strategies](https://opencode.ai/v2/docs/build/plugins#worktrees)
- [V2 canonical worktree configuration](https://opencode.ai/v2/docs/config#worktrees)
- [Git worktree](https://git-scm.com/docs/git-worktree)
- Next: [migration and acceptance](05-migration-and-acceptance.md).
