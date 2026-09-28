# DESIGN.md — Infra Manager (portal)

Design direction for the portal UI. This file is design data, not agent instructions.
Filter: `antislop.md` (core) + `skills/antislop-ui/SKILL.md`.

## Identity

- **Product**: Infra Manager, a home-server service management panel (hostname, tunnel, drift, stack).
- **User**: a single admin (Hanif), not the public. UI language: casual Indonesian.
- **Character**: workshop utility. A technician's workbench: clear labels, reachable tools,
  no display case. Data and actions matter more than ornament.
- **Not**: a landing page, a SaaS product, a Linear/Vercel clone.

## Personality

Utilitarian, honest, terse. Status is stated as-is (healthy, drift, down),
no euphoria without data. Every screen answers one working question:
"is anything broken?", "which hostnames are alive?", "which tunnel is connected?".

## Palette

Theme: **dark/light toggle**, both modes must work equally (R-21, R-34).
Why a fixed theme is not used: the admin sometimes works in a dark terminal,
sometimes in daylight; the toggle was requested outright.

Core (neutrals not counted as core colors):

- Dark neutrals: `#111417` (background), `#191d21` (panel), `#232a2f` (lines)
- Light neutrals: `#f4f2ee` (background), `#ffffff` (panel), `#ddd8d0` (lines)
- Core 1: `#1f6f4a` / light `#17593a` — workshop green, the "safe/running" color.
  Used for: healthy status, primary actions.
- Core 2: `#2b3138` dark primary text / `#20242a` light primary text.
- Single accent: `#b03a0a` (workshop orange) — only for **drift/error/delete**.
  Value raised from `#c2410c` to pass WCAG AA (4.5:1) on the light panel background.
  Never used decoratively.

Maximum 2 core + 1 accent (R-29). No gradients, no glow (R-01, R-13).

## Typography

- UI: **IBM Plex Sans**, rationale: a neutral work grotesk + has a Condensed
  variant for dense labels; not the model's default font (R-06).
- Machine (hostname, port, tunnel ID, hash, timestamp): **IBM Plex Mono**, only
  for machine values, not headings (R-06: mono is not terminal aesthetics).
- Small, tight scale: 13px base, 20px page titles. 11px uppercase labels only
  for columns/tables, not an eyebrow above H1 (R-09).
- Fallback stack stays system-ui if the font fails to load.

## Layout & composition

- RHYTHM 1: uniform, predictable structure. Compact left navbar (text,
  no lucide icons), tables/lists as the primary form, right panel for details.
  Bento grids, heroes, feature cards, and charts without asking are not used (R-05).
- One focus per screen: the drift list comes first on the Dashboard, not a row
  of equivalent statistic cards (C-3).
- Numbers only if real (R-17): hostname count, healthy container count,
  tunnel connectors from the API. No fabricated percentage deltas.
- Icons: only if they add meaning (e.g. textual status marks "OK / DRIFT /
  OFFLINE" take precedence). No sparkle/star/robot (R-04).

## Motion & energy

- **Dial: ENERGY 1 / RHYTHM 1 / MOTION 1.**
- MOTION 1: only hover/focus transitions (~120ms) and clear state changes.
  No looping animation, no pulse, no stacked fade-up (R-19).
- Status dots only for real states (healthy/drift), no glow, no pulse.

## Density & detail

- Table: columns chosen from user decisions (hostname, target, status, action).
  Per-row actions only for those that actually have behavior (R-26).
- Empty state states the cause + first action ("No services yet. Add your
  first hostname.") instead of "No data available" (R-27).
- Error state: cite the HTTP code/original API message + next step.
- Visible keyboard focus (2px accent outline, no `outline:none`) (R-32).
- Radius: 4px inputs/buttons, 6px panels. No full pill elements (R-11).
- Shadows: only modal/dropdown panels that need to be raised (R-12).
- No emoji icons in UI text.

## Key decisions (one-line rationale)

- Dark/light toggle: the admin works in two lighting conditions (R-21).
- Orange accent reserved for error/drift: bad status reads instantly without
  adding to the color count (R-29, R-31).
- Tables not cards: the user decision is comparing hostname rows (C-3).
- Mono only for machine values: reading port/ID is faster without making
  the terminal an aesthetic (R-06).
