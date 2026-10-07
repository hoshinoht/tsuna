# Building Logos in SVG

Adapted from logo-design-skill by Kaan Kızıltuğ (MIT). How to write clean, production-grade SVG by hand.

## File conventions

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256" width="256" height="256" role="img" aria-labelledby="title">
  <title id="title">Harbor logo</title>
  <path fill="#0F7C80" d="…"/>
</svg>
```
- Integer canvas: `0 0 256 256` for symbols; lockups keep height 256 and let width follow (`0 0 960 256`).
- Consistent padding (≈ 4–8 % for symbols) or tight crop — be consistent across the set.
- Integer or 1–2 decimal coordinates. Include `<title>`. Group logically (`<g id="symbol">`, `<g id="wordmark">`).
- Colours as attributes (`fill="#…"`), not `<style>` blocks. One file per variant, predictable names.

## Think in primitives first

Describe construction in words before writing path data, e.g. *"Circle r=96 at (128,128); remove a 45° wedge upper
right; a circle r=28 sits in the gap."* Pick a grid unit (8 or 16 on 256) and snap key dimensions to it; reuse the same
few radii and angles. Use `<circle>`, `<rect rx>`, `<polygon>` for primitives, `<path>` for the rest and the final silhouette.

## Paths

- `M` move, `L/H/V` lines, `A` arc, `C` cubic, `Q` quadratic, `Z` close. Prefer absolute commands while designing.
- **Arcs for circular geometry**: `A r r 0 largeArc sweep x y` (semicircle: `M 64 128 A 64 64 0 0 1 192 128`).
- **Béziers for organic curves**: anchors at extrema with horizontal/vertical handles; quarter circle handle ≈ 0.5523 × r.
- **Few anchors**; every extra point risks a wobble.
- **Exact angles** (0/15/30/45/60/90°): 30°/60° offsets use 0.5 and 0.866; 45° uses equal x/y. `svg_audit.py` flags near-misses.
- **Smooth corners** (anti bone effect): start the curve ~1.3 × corner size before the corner with handles ~0.6 × size.

## Negative space and compound shapes

- Outer contour and holes in **one path** with `fill-rule="evenodd"`:
  `<path fill-rule="evenodd" d="M128 16 A112 112 0 1 1 127.9 16 Z M128 80 A48 48 0 1 0 128.1 80 Z"/>`
- For a hidden figure, draw the negative shape first, then build positive shapes around it.
- Never fake holes with white shapes on top (they become blobs in one-colour variants).
- Avoid `<mask>`/`<clipPath>` in masters; merge touching same-colour shapes into one path (no hairline seams).

## Strokes, type, colour

- Explore with strokes; in the master, construct outlined geometry (offset by half the stroke width) or flag remaining
  strokes for expansion. Monoline strokes ≥ 8 % of mark width if it must read at 24 px.
- Inkscape, if installed, can expand and unite:
  `inkscape logo.svg --actions="select-all:all;object-stroke-to-path;path-union;export-plain-svg;export-filename:out.svg;export-do"`
- **No `<text>` in finished marks**: construct letterforms as paths (consistent stems, overshoot on rounds, thinner
  horizontals). `<text>` is fine for exploration only; flag it.
- Gradients: `gradientUnits="userSpaceOnUse"` with explicit coordinates, always alongside a flat master.

## Recipes

```svg
<!-- Letter A symbol: flat apex, 1:2 slope, constant 48-unit stroke -->
<path fill="#111" fill-rule="evenodd" d="M104 32 H152 L248 224 H200 L184 192 H72 L56 224 H8 Z M96 144 H160 L128 80 Z"/>
<!-- Equilateral triangle; centroid sits low, nudge up inside a container -->
<polygon points="128,40 229.6,216 26.4,216" fill="#111"/>
<!-- Speech bubble: circle + tail, one path -->
<path fill="#111" d="M128 24 A104 104 0 1 1 61.6 208 L28 236 L37.9 180 A104 104 0 0 1 128 24 Z"/>
<!-- App-icon tile, ~22 % radius; symbol at 60–70 % of the tile -->
<rect width="256" height="256" rx="56" fill="#0F7C80"/>
```

## Avoid in a master

| Avoid | Instead |
|---|---|
| `<text>` | outlined paths |
| `<image>` / embedded raster | vector paths |
| filters (blur, shadow, glow) | flat shapes; darker shape for shadow |
| masks, clipPaths, deep nested transforms | baked geometry; at most one wrapper transform |
| > 3 colours, many gradients | ≤ 3 flat colours |
| details < 1/48 of the mark | merge or remove |
| angles a degree off | exact angles |
| editor metadata, excess precision | clean markup |
