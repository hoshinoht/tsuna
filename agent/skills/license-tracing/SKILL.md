---
name: license-tracing
description: Trace who wrote what and under which license before relicensing, publishing, vendoring or copying code or prompts — per-author git blame, license compatibility (GPL-2.0-only vs -or-later, GPL-3.0, MIT, Apache-2.0, source-available licenses), NOTICE/attribution, and replacing incompatible parts by rewriting from behaviour specs. Use when forking away from an upstream, changing a license, vendoring third-party files, or borrowing from another project.
license: GPL-3.0-or-later
metadata:
  domain: "licensing"
---

# License tracing

Not legal advice. The goal is to know, with evidence, what can be kept, what
needs attribution, and what must be replaced — and to say plainly where
certainty ends.

## 1. Find the license of every source

- Read the top-level `LICENSE`, then look for **per-directory licenses**:
  vendored folders often carry their own (`LICENSE`, `LICENSE-*.txt`,
  `ATTRIBUTION.md`, `SOURCE`, SPDX headers, `license:` in skill frontmatter or
  `package.json`). A repo under a restrictive license can contain MIT or
  Apache parts, and the reverse.
- Classify each license:
  - Permissive (MIT, BSD, Apache-2.0): keep with the notice; Apache also keeps
    NOTICE text.
  - Copyleft: note the exact version and whether "or later" is granted.
    GPL-2.0-only code cannot be combined into a GPL-3.0 project.
  - Source-available / non-OSI (for example the Sustainable Use License):
    usually not compatible with GPL distribution. Learn from the ideas;
    don't copy or closely paraphrase the text.

## 2. Trace authorship

- `git log --format='%an' | sort | uniq -c` for the contributor picture.
- Per file and per line: `git blame --line-porcelain -- <file>` and count
  lines by author. Files with only trivial lines from others (`---`, blank
  lines, keys) are effectively the owner's; files with substantial prose or
  code from others need action.
- Blame undercounts: reformatted or moved lines show the last editor. Treat
  counts as a floor and mention it.
- Check copied assets too (templates, images, logos): trademarks and
  third-party templates keep their own terms.

## 3. Decide per item

| Situation | Action |
|---|---|
| Owner's own work | Keep; license as the owner chooses |
| Permissive third party | Keep; add to `NOTICE` with name, license and path; ship its license file |
| Compatible copyleft ("or later") | Keep under the new version if the grant allows; record it |
| Incompatible (GPL-2.0-only into GPL-3.0, source-available) | Replace, or get written permission from the authors |

The only certain route for someone else's incompatible code is written
permission. Rewriting reduces risk but does not erase it — say so.

## 4. Replacing incompatible parts

1. A spec writer reads the original and writes a **behaviour-only spec**:
   interfaces, formats, error strings that tests assert, edge cases — no
   copied code, comments or prose.
2. A separate implementer works **only from the spec** and the owner's own
   code, never opening the original.
3. Verify with the owner's tests and, where useful, by running the original
   as a black box (outputs are data, not code).
4. Keep the original out of the new repository and history; start fresh
   history if the old one contains the incompatible code.

## 5. Record it

- `NOTICE` lists every third-party component with path, license and
  copyright; bundled license files sit next to the component.
- `package.json` / frontmatter `license` fields match the actual license.
- Before publishing, re-scan (step 1–2) and mention anything uncertain.
