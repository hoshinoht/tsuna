---
name: docs-workflow
description: Drive the pandoc-based docs plugin to produce reports, notes and styled PDFs (or DOCX/HTML) from markdown, with citations kept in a refs.bib sidecar and output shaped by presets (SIT/UofG school report, Eisvogel). Use when the user wants a report, styled PDF, bibliography or citation formatting (IEEE, APA, ACM styles) through the docs_* tools. Venue papers are written in native LaTeX with the official class instead.
compatibility: Requires OMP with the Hoshi docs extension (docs_* tools), pandoc and a LaTeX distribution
metadata:
  domain: "documents"
  workflow: "pandoc"
---

# Docs workflow

This skill covers the `docs_*` tools: drafting and iterating on documents,
compiling them to PDF with a preset, and handling bibliographies and citation
styles. Load it whenever the user wants a report, styled PDF, bibliography
or formatted citations.

**Not for venue papers.** Anything submitted to a conference or journal is
written in native LaTeX with the venue's official class (`IEEEtran`,
`acmart`, `llncs`, ...), not through a pandoc template. In this setup that is
the `scholar` agent's job.

## Default route: drafts

Unless the user plainly wants a one-off conversion, work through a draft:

1. `docs_templates_list` to see which templates and CSL files are installed.
2. `docs_templates_install` for anything the chosen preset or style needs.
3. `docs_draft` to create the working folder `.opencode/docs/<id>/`.
4. Edit `.opencode/docs/<id>/draft.md` with OMP's live `edit` or `apply_patch` tool; retain hashline anchors from `read` when that edit mode is active.
5. Put references in `.opencode/docs/<id>/refs.bib`.
6. `docs_compile` to render the PDF.

Why this route: the content stays in markdown the whole time, and the model
never has to write or maintain a complete LaTeX project.

## Picking a tool

**Iterative work**

| Tool | Purpose |
|---|---|
| `docs_draft` | create a reusable draft workspace |
| `docs_compile` | render a draft to PDF with its stored preset |
| `docs_list_drafts` | list active drafts |
| `docs_delete_draft` | remove a draft; only when the user asks |

**One-shot work**

| Tool | Purpose |
|---|---|
| `docs_create` | PDF in one call; the preset picks school report or Eisvogel styling |
| `docs_compile_latex` | build an existing `.tex` file |
| `docs_convert` | format conversion only |

## Citations and bibliography

- Keep references in a `refs.bib` sidecar wherever you can, not inline in the body.
- Cite with Pandoc syntax, for example `[@lamport1978time]` or `[@lamport1978time; @fischer1985impossibility]`.
- `docs_compile` and `docs_create` pick up a `refs.bib` sitting next to the markdown or draft automatically. You can still pass `bibliography` explicitly.
- Choose `citation_style` deliberately:

| Value | Behaviour |
|---|---|
| `default` | the preset decides; the built-in presets use citeproc with a CSL file when one is configured |
| `ieee` | IEEE-style citations through CSL (`csl-ieee`) |
| `apa` | APA through CSL |
| `acm` | ACM through CSL |
| `none` | no citation processing; citation syntax is left as written |

**Backends differ.** The built-in presets stay markdown-first with citeproc
and CSL. A user YAML preset that opts into natbib instead generates `.tex` and
runs `pdflatex` → `bibtex` → `pdflatex` → `pdflatex`. If a CSL file or template is missing, install it
with `docs_templates_install`; do not guess at a substitute.

## Presets

| Preset | Output |
|---|---|
| `school-report` | SIT/UofG report |
| `eisvogel` | polished general-purpose PDF |

When the user is unsure which fits, show them `docs_presets_list` and
`docs_presets_show` (as a subagent, summarise them in your report instead).

## Writing the source

- Default to markdown with only a light touch of raw LaTeX. Prose and tables belong in markdown; reserve raw LaTeX for equations and genuine edge cases. Do not write full custom TeX unless the user asks for LaTeX source.
- For school reports, pass the `authors` JSON argument to `docs_compile`; it fills the dedicated author table on the title page.

## Worked flows

**Iterating on a school report**

`docs_templates_list` → `docs_draft(title="Evaluating Cache Replacement Policies", preset="school-report")`
→ write `draft.md` → add entries to `refs.bib`
→ `docs_compile(doc_id="<id>", logo="sit", citation_style="ieee")`

**APA-style report**

Check that `csl-apa` is installed (install it if not) → prepare the markdown and
`refs.bib` → `docs_create(..., preset="eisvogel", citation_style="apa")`, or take
the draft route and pass `citation_style="apa"` to `docs_compile`.

**Keeping citations literal**

Pass `citation_style="none"`.

## Decision rules

- The user will revise the document: use a draft.
- A paper or report is requested with no format: ask for a preset when an interactive ask tool is available, or recommend one. Otherwise use the brief's stated preset or the obvious default (`school-report` for SIT/UofG coursework, otherwise `eisvogel`) and name the choice in your report.
- Scholarly formatting is needed: ask which citation style when an interactive ask tool is available, unless the preset or brief already implies it. Otherwise use the brief's style or `default`, and name the choice in your report.
- A venue paper (IEEE, ACM, ...): hand it to native LaTeX with the official class; do not use a preset.
- APA report: citeproc with `apa.csl`.
- Install missing templates before compiling.
- Never delete a draft unless the user asks.

## Common mistakes

- Writing a full LaTeX document where markdown would have done.
- Pasting bibliography entries into the body when `refs.bib` is available.
- Approximating a venue's paper format with a pandoc preset.
- Compiling before the needed CSL file or template is installed.
- Using `docs_compile_latex` on a markdown-first document when `docs_compile` or `docs_create` is the right choice.
