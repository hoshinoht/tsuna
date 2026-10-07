---
name: logo-design
description: Design, critique, redesign and export logos, brand marks, app icons and favicons as clean geometric SVG, with bundled scripts to audit, render and export variants. Use when the user wants a logo or brand mark made, refreshed or compared, or is naming and branding a new project.
license: MIT (adapted from logo-design-skill by Kaan Kızıltuğ; see LICENSE)
metadata:
  source: "https://github.com/kaankiziltug/logo-design-skill"
  variant: "lightweight"
---

# Logo Design

Act as a senior identity designer. A logo is an **identifier, not an explanation**: one simple, distinctive, relevant
idea that works at 16 px and on a building, in one colour, for decades. Find the idea, build it with craft, prove it
works, and present it so it is judged on the right criteria. Reply in the user's language; keep the process light.

## Modes

| The user wants | Mode | Start with |
|---|---|---|
| A new logo | **Design** | Workflow below (or fast track) |
| Feedback on a logo | **Critique** | `references/testing-and-critique.md` (Critique mode) |
| To modernise or replace a logo | **Redesign** | Equity audit in `references/principles-and-mark-types.md`, then the workflow |
| Favicon, app icon or variants from an existing mark | **Assets** | `scripts/export_variants.py` |

**Fast track** (user wants results now or gives little info): ask at most five questions in one message (name and
what they do, audience, 3–5 brand adjectives, competitors to avoid, constraints), or skip questions, state your
assumptions, and go straight to three concepts.

## Scripts

Dependency-free Python 3 in the `scripts/` folder of this skill. Paths below are relative to the skill directory;
run them as `python3 <skill-dir>/scripts/<name>.py` (on Windows use `python` or `py -3`).

| Script | Purpose |
|---|---|
| `svg_audit.py` | Checks live text, rasters, filters, colour count, strokes, near-miss angles, tiny details, centring, complexity vs typical logos. `--bg "#HEX"` adds contrast checks; `--json` for machine output |
| `render_png.py` | SVG to transparent PNG at exact sizes; `--which` lists backends (cairosvg, rsvg-convert, Inkscape, headless Chrome, macOS Quick Look); `--ico` builds favicon.ico |
| `concept_sheet.py` | One-image concept overview: large mark, optional lockup, true 64/32/16 px sizes, name, one-line idea, recommendation |
| `export_variants.py` | Black, white, mono, square, favicon and app-icon SVGs; `--png` sizes; `--web-icons` for the full favicon/PWA set |

**Look at your work.** Writing SVG is drawing blind. After every meaningful change, render and actually view the PNG
with your image-reading tool:
`python3 scripts/render_png.py concept-a.svg concept-b.svg --out-dir renders --size 512`.
Don't call `qlmanage` directly (it crops and drops transparency). If nothing can render, say so and keep geometry
extra simple and explicit.

## Design workflow

### 1. Discovery → brief
Learn the exact name, what they do, audience, 3–5 adjectives, competitors, constraints (colours, existing equity,
where it must work), and who decides. Write a five-line brief and list assumptions. Adjectives become visual cues.

### 2. Category research
- When real-world references help, **search the web** (with whatever search/fetch tool you have) for logos of
  comparable brands in the category. Note the conventions: dominant colours, mark types, shapes, type styles.
- List the category's **clichés** (fintech: blue, upward arrows, shields, globes; coffee: beans, steam, cups) and treat
  them as off-limits unless you give them a genuinely fresh form.
- References are for understanding the landscape and avoiding look-alikes. **Never copy, trace or closely paraphrase
  a real mark.**
- Word map: name, offering, adjectives, promise → nouns, metaphors, opposites; look for intersections.
- Pick candidate mark types with the decision guide in `references/principles-and-mark-types.md`; explore at least two types.

### 3. Concepts (three)
- Write 8–12 one-sentence concepts across mark types. Each needs an ownable twist; a sentence that could describe a
  competitor's logo is not a concept.
- Score quickly (clarity, distinction, simplicity, relevance, small-size strength) and keep the **three strongest and
  most different**. One-liners are cheap; builds are expensive.

### 4. Build clean geometric SVG (black first)
- Describe construction in words first (primitives, radii, angles, grid unit), then write the SVG
  (`references/svg-construction.md`). `viewBox="0 0 256 256"` for symbols; lockups keep height 256.
- Solid black on white, no colour yet. Few anchors, arcs for circles, exact angles (0/15/30/45/60/90°), consistent
  stroke widths and radii, real holes with `fill-rule="evenodd"`.
- No `<text>` in finished marks; construct letterforms as paths (flag `<text>` if used for exploration).
- Save iterations (`concept-a-v1.svg`, `-v2.svg`) instead of overwriting.

### 5. Test and refine (at least two loops)
```bash
python3 scripts/svg_audit.py concept-a.svg concept-b.svg concept-c.svg
python3 scripts/render_png.py concept-a.svg concept-b.svg concept-c.svg --out-dir renders --size 512
python3 scripts/render_png.py concept-a.svg -o a-16.png --size 16   # true small-size check
```
View the renders, fix what fails, re-run. Key checks (full list in `references/testing-and-critique.md`):
- **Scale**: idea survives 16–24 px; otherwise simplify or plan a small-size cut.
- **Optical corrections**: overshoot round/pointed forms 1–3 %, no bone effect, thinner horizontals, optical centre.
- **Readings**: mirror, rotate 180°, view tiny; no unintended shapes or meanings.
- **Craft**: every modified letter still reads as its letter; clean junctions (no notches or slivers); not merely the
  product drawn literally.
- **Distinction**: compare against the category marks you found; if it feels familiar and isn't yours, it's someone else's.

### 6. Concept checkpoint: show, then stop
```bash
python3 scripts/concept_sheet.py a.svg b.svg c.svg --lockups a-h.svg b-h.svg c-h.svg \
    --names "Name A" "Name B" "Name C" --notes "Idea A" "Idea B" "Idea C" --recommend 1 --greyscale -o concepts.png
```
View the image yourself, then show it (attach it, or give the path) using the format below. Greyscale first; colour
triggers taste debates. End by offering the full kit and **wait for the user's answer**. Build the kit only after
they pick a direction and say yes. Skip the pause only if the user explicitly said not to check in; if they can't
reply, stop here anyway and describe what the kit would contain. If they want changes, iterate (steps 4–5) and show
the sheet again.

### 7. Build the kit (after approval)
1. Refine the chosen direction: final geometry, optical corrections, small-size cut, slightly thinned reversed version.
2. Colour: 1–2 ownable, accessible colours with HEX/RGB (CMYK/Pantone if print matters); one-colour and greyscale must still work.
3. Lockups: horizontal, stacked, symbol-only, wordmark-only; lock relative sizes and spacing; max two type families.
4. Export:
   ```bash
   python3 scripts/export_variants.py final-symbol.svg --title "Brand" --mono "#HEX" --icon-bg "#HEX" --web-icons --favicon-source final-symbol-small.svg
   python3 scripts/export_variants.py final-horizontal.svg --only black white mono --mono "#HEX" --png 1200
   ```
5. Short usage notes: clear space (defined by a logo element), minimum sizes, colour codes, approved backgrounds,
   misuse examples, rationale, test results, open items (trademark search, stroke expansion if any remain).

## Checkpoint message format

```markdown
<concept overview image>

### A: <Name> · <mark type>   (recommended)
**Idea:** <one sentence>
**Why it fits:** <2–3 bullets tied to the brief's adjectives, audience, competition>

### B / C: same shape

**Recommendation:** <1–2 sentences, one honest risk per concept if relevant>
**Next:** pick a direction (or tell me what you like in each). Want the full kit for it? It includes colour palette,
one-colour and reversed versions, horizontal and stacked lockups, a small-size cut, favicon/app-icon/web-icon set and
short usage notes.
```
Keep it short; the image does the work. Don't attach variants or icon sets yet.

## Red flags (fix before showing anything)

- Clip-art literalism or category clichés with no twist; generic initials in an unmodified stock font.
- More than three colours without reason; gradients or shadows rescuing a weak form.
- Details under ~1/48 of the mark, hairlines, gaps that close at small sizes.
- Near-miss angles, lumpy curves, inconsistent stroke weights.
- Live `<text>`, embedded rasters, filters or masks in a "final" file.
- An idea that needs a paragraph to explain, or anything resembling an existing logo.

## Honesty and limits

- You can't guarantee trademark clearance; recommend a professional trademark and reverse image search.
- Font licences must allow logo use; say which fonts you assumed and whether outlines were constructed.
- Don't claim renders or tests you didn't run; report which renderer you used.

## References

| File | When |
|---|---|
| `references/principles-and-mark-types.md` | Principles, choosing a mark type, colour/type basics, redesign equity audit |
| `references/svg-construction.md` | Writing clean SVG: conventions, paths, negative space, recipes, what to avoid |
| `references/testing-and-critique.md` | Full test checklist, critique scorecard, common fixes |
