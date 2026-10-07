---
name: write-readme
description: Write or restyle a project README in the owner's house style — centered header with tagline and badges, principle quote, status callout, "At a glance" table, grouped features, small architecture diagram, quick start with a next-steps table, security callouts, development commands, collapsible layout, non-goals, license; concise, no emojis, details moved to docs/. Use when writing, restyling or reviewing a README or top-level project page.
license: GPL-3.0-or-later
metadata:
  domain: "documentation"
---

# Write a README

Write READMEs that a newcomer can scan in under a minute and an existing user
can act on immediately. The README is the front page, not the manual: anything
longer than a short section moves to `docs/*.md` and is linked.

## Rules

- **No emojis** anywhere (headings, tables, bullets, badges).
- **Concise.** Short sections, tables where they help, no walls of text, no
  marketing filler. Prefer one precise sentence over three vague ones.
- **Verified.** Every command, flag, version, count, path and claim must match
  the repository right now (check `--help`, manifests, config files, the file
  tree). Don't document planned features as present; mark status honestly.
- **Details elsewhere.** Architecture notes, full option references, install
  edge cases and design history belong in `docs/`, linked from the README.
- **No private data.** No personal paths, hostnames, tailnet names, private
  project names or secrets; use placeholders such as `<machine>` or `~/…`.

## Structure (in this order; drop sections that don't apply)

1. **Centered header** (`<div align="center">` … `</div>`):
   - `# Name` (add a short meaning in parentheses if the name has one).
   - A **bold one-line tagline** saying what it is.
   - An *italic subline* with context (what it's built for, where it runs).
   - 3–4 shields.io badges for facts that matter (version, language/runtime
     version, license, key compatibility), each linking to its source.
   - A nav line of section links separated by ` · `.
2. **Principle quote**: a `>` blockquote with one bold sentence stating the
   project's core idea, optionally one follow-up line on what it does *not* own.
3. **Status callout**: `> [!NOTE]` with maturity (beta, pre-1.0, stable) and
   compatibility promises.
4. **At a glance**: a two-column table with an empty header row (`| | |`) and
   bold row labels (entry points, backends, formats, platforms, …).
5. **Features**: `###` subsections by theme; bullets start with a **bold
   lead-in:** followed by one sentence. Use a table when comparing variants.
6. **Architecture**: one small Mermaid `flowchart` showing the real data or
   control flow, plus one or two sentences and a link to deeper docs.
7. **Quick start**: a **Requirements:** line, then one code block of numbered
   `# 1. …` steps that actually run, then a **Next steps:** table
   (`| Step | Guide |`) linking to docs or giving the exact command.
8. **Usage / API** (if applicable): one realistic example and its output shape.
9. **Security**: GitHub callouts where they fit — `> [!IMPORTANT]`,
   `> [!WARNING]`, `> [!CAUTION]` — then short bullets.
10. **Development**: one code block with the real check/test/build commands.
11. **Repository layout**: inside `<details><summary>Repository layout</summary>`
    as a `| Path | Contents |` table.
12. **Non-goals**: one short paragraph listing what the project deliberately is not.
13. **License**: one line linking the license file (and NOTICE for third-party parts).

## Checklist before finishing

- [ ] No emojis; no private paths, names or secrets.
- [ ] Every command and flag copied from the real CLI/help output.
- [ ] Badges and version numbers match the manifests.
- [ ] Every relative link resolves to an existing file or anchor.
- [ ] Long material moved to `docs/` and linked.
- [ ] Mermaid diagram reflects the actual components.
