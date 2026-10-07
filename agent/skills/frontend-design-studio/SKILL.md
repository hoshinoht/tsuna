---
name: frontend-design-studio
description: Load for new or substantially reshaped frontend UI when the work needs an intentional visual direction, distinctive typography, palette, composition, copy, or motion. Do not load for routine frontend maintenance, wiring, bug fixes, or accessibility-only changes.
metadata:
  domain: frontend
  related-skills: frontend-design
---

# Frontend Design Studio

Use this skill as the visual-design companion to `frontend-design`. The
`frontend-design` skill remains authoritative for information architecture,
interaction semantics, accessibility, states, and recovery. This skill owns
the visual thesis and the discipline required to avoid generic, templated UI.

## When to use

Load this skill when the request involves a new screen, a substantial visual
reshaping, a design system or visual language, typography, colour, layout,
illustration, animation, or interface copy. Do not load it merely because a
frontend file is being edited.

## Design brief

Before implementation, identify:

- the subject, audience, and primary job of the interface;
- the tone and one memorable visual decision;
- framework, performance, accessibility, responsive, and content constraints;
- the real content and edge cases that will expose hierarchy and layout flaws.

If the brief does not identify the subject, propose one concrete interpretation
and ask for confirmation when the choice materially changes the design. Without
an interactive ask tool, return BLOCKED with the proposed
interpretation instead of building on it.

## Visual system

Create a compact plan before writing UI code:

- **Colour:** 4–6 named semantic values with a clear dominant colour story,
  defined as CSS custom properties (palette plus semantic tokens) with
  deliberate accents.
- **Type:** deliberate display and body roles, or one family used with
  intentional scale, weight, width, and spacing. Avoid default-looking stacks
  such as Arial, Inter, Roboto or a bare system stack unless the brief earns
  them. Set hierarchy through scale, weight, spacing and rhythm.
- **Layout:** a one-sentence composition concept and a small ASCII wireframe.
  State whether content is left-aligned, centred, justified, dense, or airy.
  Asymmetry, overlap or a strong grid are available when they serve the
  subject; a template layout is a choice, not a default.
- **Atmosphere:** background, texture, depth or pattern that sets the mood.
  A flat surface is fine when minimalism is the concept.
- **Principles:** the choices that make this interface specific to its subject.

Review the plan against the brief. If it resembles a generic answer for the
same product category, revise the direction before coding. Maximalist and
minimalist directions are both valid; maximalism needs depth and minimalism
needs precision.

If the visual direction is unclear but the subject is known, weigh two or three
distinct directions, choose the one the product context best supports, and
record it as an assumption the user can change.

## Distinctiveness guardrails

- Make the subject matter drive visual choices; do not apply a universal
  palette or layout.
- Use one memorable element and keep supporting structure disciplined.
- Treat borders, dividers, labels, numbering, and badges as information
  structures, not decoration.
- Do not default to warm cream plus high-contrast serif, acid accent on black,
  broadsheet rules, identical rounded cards, gradient washes, tracked
  all-caps eyebrows, middle-dot metadata, or arrow-suffixed buttons unless the
  brief earns that choice.
- Avoid single-word headline accents, unnecessary labels, and generic
  fade-and-slide entrances on every section.
- Use motion sparingly: one orchestrated reveal or interaction-led transition
  is preferable to scattered effects. Prefer CSS for simple builds and the
  framework's motion library when the project has one. Respect reduced motion.
- If the build drifts toward generic or inconsistent styling, find where it
  drifted, re-anchor typography, colour, spacing and motion to the thesis, and
  re-check responsiveness and accessibility afterwards.

## Content and interaction

Write from the user's perspective using plain, active, sentence-case language.
Name actions by their result (`Save changes`, `Publish`) and keep vocabulary
consistent through loading, success, and error states. Errors state what
happened and how to recover; empty states provide a useful next action.

Preserve the companion skill's requirements for responsive behavior, keyboard
focus, accessible contrast, touch targets, semantic structure, and performance.
Render early when possible, critique the result against the brief, remove one
unnecessary decorative choice, then verify desktop, mobile, and reduced-motion
behavior.

## Required handoff

Return:

1. the design brief and visual thesis;
2. the colour, type, layout, and principles plan;
3. the implementation and any intentional deviations;
4. responsive, accessibility, motion, and visual validation evidence;
5. unresolved assumptions or blockers.

When `frontend-design` is also loaded, use its final evidence-table format.
