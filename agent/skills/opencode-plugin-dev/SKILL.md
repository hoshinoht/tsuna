---
name: opencode-plugin-dev
description: Build, change and verify OMP (hoshi-omp) plugins (local packages using @opencode/plugin) without breaking a live setup — dependency installs, version pinning, hot reload into running sessions, transform ordering against the config layer, invisible service logs, and checking results through `opencode api` without model calls. Use when writing or debugging an OMP (hoshi-omp) plugin or its registration in opencode.json.
compatibility: OMP (hoshi-omp).x with bun; written against 2.0.20.
license: GPL-3.0-or-later
metadata:
  domain: "opencode"
---

# OMP (hoshi-omp) plugin development

## Dependencies and versions

- OMP (hoshi-omp) does **not** install dependencies for local plugin packages. A
  missing install shows up as `Cannot find package '@opencode/plugin'` in
  `~/.local/share/opencode/log/opencode.log` and the plugin reports
  `failed`. Run `bun install` at the workspace root after any `package.json`
  change.
- Pin `@opencode/plugin` (and `@opencode/client`) to the **installed**
  OpenCode version (`opencode --version`) in every package. Mixed pins mean
  mixed API types and runtime behaviour.
- Read the real `.d.ts` files under `node_modules/.bun/@opencode+plugin@<v>*/`
  before relying on an API. Types and runtime can drift: for example 2.0.20
  exposes `ctx.catalog` where the types declare `ctx.model`; support both.

## Live servers hot-reload plugin code

- A running `opencode serve` watches local plugin files and reloads the plugin
  **into live sessions** as soon as a file changes. Editing plugin source is a
  live change for anyone using that server.
- While the user is working, batch edits, keep each save valid (typecheck
  first), and verify the live state afterwards.
- A reload rebuilds agents and models by replaying transforms in registration
  order, and the host's config layer (`opencode.config.agent`,
  `opencode.config.provider`) can re-register **after** your plugin. If your
  transform must win (for example setting agent models), detect it — your
  transform sees agents defined in `agents/*.md` missing — then re-register
  your transform and call `ctx.agent.reload()`, with a bounded retry count.

## Logs and verification

- In service mode (`opencode serve --service`) plugin `console.*` output is
  not visible anywhere. Write important events (applied changes, validation
  errors with `file:line:col`, recoveries) to your own small log file under
  `$XDG_STATE_HOME/opencode/` and never throw if it can't be written.
- Verify without model calls:
  - `opencode api GET /api/plugin` — every plugin's `active`/`failed` state
    and error ref (details in the server log).
  - `opencode api GET /api/agent`, `/api/skill`, `/api/command`,
    `/api/config` — effective agents and models, discovered skills and
    commands, resolved config (including `{env:VAR}` expansion).
  - `opencode api POST /api/location/reload` — force a reload.
- `opencode api` starts the background service if none is running. Stop only
  what you started (track the PID); never `pkill` by pattern while the user
  may have a session open.
- For runtime tests that need a real host, start a **private** server:
  isolated `HOME`/`XDG_*`/`TMPDIR` under a scratch directory, its own port,
  a scratch config registering only the plugin under test. Never touch the
  user's config or service.

## Registration and config

- Register local packages in `opencode.json` `plugins` as
  `{ "package": "./packages/<name>", "options": { … } }`; validate options at
  load and fail with a message naming the bad option.
- Paths in options: use `{env:HOME}` rather than absolute user paths.
- Keep plugin-owned data files (for example YAML presets) outside
  `opencode.json`, re-read them on reload, and keep the last good version on
  a parse error.
- Global instruction text goes in the config root `AGENTS.md`; OMP (hoshi-omp)
  accepts but ignores the `instructions` key. Custom agent prompts replace the
  provider base prompt, so shared harness rules must live in `AGENTS.md`.

## Checklist

- [ ] `bun install` done; pins match `opencode --version`.
- [ ] Typecheck and tests pass before saving into a live setup.
- [ ] `GET /api/plugin` shows the plugin `active`; effective state checked via
      `/api/agent` or the relevant endpoint.
- [ ] Only processes you started were stopped.
