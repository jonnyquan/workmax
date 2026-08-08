# Design System — Tech Utility

> Bloomberg Terminal / Linear's data views / Datadog dashboards
> lineage. Information density, monospace-forward, terminal energy.
> Best for B2B PPT, dashboard mockups, data-heavy slides.

`derived_from: tech_utility` (no fallback_5 entry — picker option for data-heavy modes)

## 1. Color

| Slot | OKLch | Hex | Role |
|---|---|---|---|
| bg | oklch(98% 0 0) | #f7f7f7 | neutral light |
| fg | oklch(15% 0 0) | #1a1a1a | terminal text |
| accent | oklch(55% 0.20 195) | #00a3a3 | teal — terminal cursor |
| muted | oklch(70% 0 0) | #a8a8a8 | metadata, inactive |

Status palette (data viz):
- pos:   oklch(60% 0.18 145) / #2da34a (green)
- neg:   oklch(55% 0.20 25)  / #c64242 (red)
- warn:  oklch(75% 0.18 80)  / #e6b13d (amber)
- info:  oklch(60% 0.18 230) / #3a82e6 (blue)

## 2. Typography

- Display: JetBrains Mono, Söhne Mono, monospace · weight 500 / 700 · sizes [40, 28, 22, 18]
- Body:    Inter, system-ui, sans-serif · weight 400 · sizes [14, 13, 12]
- Mono:    JetBrains Mono · weight 400 · size 13 (default for code, numbers, identifiers)

Tabular numerals everywhere — `font-variant-numeric: tabular-nums`. Tracking: 0 mono, 0.01em body.

## 3. Spacing

Scale: 4 / 8 / 12 / 16 / 24 / 32 / 48 (terminate small — density wins)
Grid: 16-col (denser than minimal), gutter 16

## 4. Layout

- Container: max-w 1440 (wide for data tables)
- Breakpoints: sm 640 · md 768 · lg 1024 · xl 1280 · 2xl 1600
- Tables: full-bleed when needed, sticky headers
- KPI cards: 4-up grid, monospace numerals, 32px metric value

## 5. Components

- Button (primary): bg accent, fg bg, radius 4, padding 6/12, weight 600 mono, size 13
- Card: bg bg, 1px border muted, radius 4, padding 16, no shadow
- Input: 1px border muted, radius 4, padding 8/12, mono font, focus: 1px ring accent
- Badge: pill 24px height, mono font, status palette colors

## 6. Motion

- Fast: 80ms
- Default: 140ms
- Slow: 200ms
- Easing: linear or ease-out — instrumental motion only

## 7. Voice

Tone keywords: precise · technical · low-decoration · numbers-first
- Do say: exact figures, units, latencies, percentiles
- Don't say: marketing language, hedge words, emojis

## 8. Brand

- Logo monogram or wordmark in mono color
- Clear-space ≥ 1× logo cap-height
- Don'ts: no glow, no gradient, no 3D

## 9. Anti-patterns

- Serif display (breaks terminal frame)
- Pastel palette (loses precision feel)
- Soft shadows / blurs
- Round-everything (radius > 4 is wrong)
- Decorative iconography
