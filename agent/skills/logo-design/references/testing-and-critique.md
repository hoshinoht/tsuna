# Testing and Critique

Adapted from logo-design-skill by Kaan Kızıltuğ (MIT). Run the tests on every concept before showing it and again on
final artwork. `scripts/svg_audit.py` covers structure and geometry; render with `scripts/render_png.py` and look for
the rest.

## Test checklist

**Scale**
- [ ] 16 px: core idea survives. If not, make a simplified small-size cut (fewer elements, thicker strokes, bigger gaps).
- [ ] 24–32 px: recognisable at a glance. Very large: smooth curves, no lumpy anchors.
- [ ] Minimum size documented (px for screen, mm for print).

**Colour and value**
- [ ] One-colour black on white, and white on black (thin the reversed version slightly if it looks heavier).
- [ ] On the brand colour and on a photo (add a container/outline version if needed).
- [ ] Greyscale: parts separate by value; nothing relies on hue alone.

**Form**
- [ ] Squint/blur: the silhouette alone is distinctive.
- [ ] Mirror and rotate 90°/180°: no hidden proportion errors or unintended readings (letters, body parts, offensive,
      political or religious symbols, hazard signs).
- [ ] Optical corrections: overshoot round/pointed forms 1–3 %, fix bone effect, thin horizontals, centre slightly above
      geometric centre.
- [ ] Clean geometry: no near-miss angles, consistent radii and stroke widths. Balanced, not accidentally tilted.
- [ ] **Letter test**: each customised letter still reads as the intended letter at first glance.
- [ ] **Junctions**: no notches, slivers, lumps or hairline gaps where strokes meet.

**Distinctiveness**
- [ ] Shelf test: next to competitors (found via web search) it stands out rather than blending in.
- [ ] Familiarity test: if it feels familiar and isn't yours, it's someone else's. Never copy or trace a real mark.
- [ ] Recommend a professional trademark search and reverse image search; you cannot give legal clearance.

**Meaning and fit**
- [ ] One-sentence idea; tone matches the brand adjectives; identifies rather than explains.
- [ ] Colour and symbol meanings checked for the audience's cultures.

**Production**
- [ ] Master is vector only: text outlined, strokes expanded, no filters/rasters, clean viewBox (audit score ≥ ~90, no FAIL).
- [ ] Survives embroidery (no hairlines), circular avatar crop, app-icon tile, dark mode.

Record results when presenting: what passed, what was adjusted ("opened the counter gap from 6 to 10 units so it
survives 16 px"), and what the user must still do (trademark search, colour proofing).

## Critique mode

1. **Context first**: what the organisation does, for whom, how it should feel. Ask one question or state assumptions.
2. **First impression** (2 seconds): what a stranger would see and call it.
3. **Technical pass**: for an SVG, run `svg_audit.py` and render it; for a raster, reason visually about the same checks.
4. **Score** each dimension 1–5 with one line of evidence.
5. **Top 3 changes**, highest impact first, each specific and actionable. Optionally demonstrate with a revised SVG.

| Dimension | Question |
|---|---|
| Idea | One clear, relevant idea, sayable in a sentence? |
| Simplicity | Anything unnecessary? Survives 16 px and a squint? |
| Distinction | Stands apart from competitors and famous marks? |
| Memorability | One defining feature you'd recall tomorrow? |
| Relevance / tone | Shape, weight, colour, type match the brand? |
| Craft | Clean geometry, optical corrections, consistent weights, spacing? |
| Versatility | One colour, reversed, small, large, app icon, lockups? |
| Longevity | Conceptual rather than trendy? |
| Colour | Few, purposeful, ownable, accessible? |
| Typography | Appropriate, customised, legible, ≤ 2 families? |

Total /50 is a rough guide; one fatal flaw (illegible small, looks like a competitor, offensive reading) outweighs it.

**Common fixes**
| Symptom | Fix |
|---|---|
| Mush at small sizes | Remove details, thicken strokes, open gaps; small-size cut |
| Generic / template-like | Customise letters, find an ownable twist from the name or promise |
| Dated | Remove gradients, bevels, shadows; more timeless type |
| Busy | Keep one idea; move others into the wider identity |
| Symbol and type unrelated | Share radii, weights, angles; rebalance sizes |
| Unstable | Widen the base, fix axes, check optical centre |
| Circle looks small beside letters | Add overshoot |
| Reversed looks bold | Thin the white version |

Output shape: **First impression**, **What works** (2–3 bullets), **Scorecard**, **Top 3 changes**, **Optional next step**.
Critique the work, never the person.
