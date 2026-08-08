# Design System — Editorial Magazine

> Monocle / FT Weekend / NYT Magazine lineage. Print-magazine feel
> applied to digital surfaces. Serif display, generous margins, warm
> palette, considered hierarchy.

`derived_from: editorial_magazine` (visual-directions.yaml)

## 1. Color

| Slot | OKLch | Hex | Role |
|---|---|---|---|
| bg | oklch(98% 0.01 75) | #fbf9f5 | cream page |
| fg | oklch(20% 0.01 75) | #252320 | ink |
| accent | oklch(55% 0.15 30) | #b75240 | warm rust — pull-quote, drop-cap |
| muted | oklch(70% 0.02 75) | #b3aea4 | byline, caption |

Semantic:
- success: oklch(50% 0.10 130) / #5a8043
- warning: oklch(65% 0.13 60)  / #c98a3f
- error:   oklch(50% 0.18 25)  / #b6402f

## 2. Typography

- Display: Söhne Breit, Tiempos Headline, Georgia, serif · weight 700 · sizes [56, 42, 32, 24]
- Body:    Inter Tight, system-ui, -apple-system, sans-serif · weight 400 / 500 · sizes [17, 15]
- Mono:    JetBrains Mono · weight 400 · size 13

Tracking: -0.015em display, 0 body. Line height: 1.1 display, 1.6 body (long-form reading).

## 3. Spacing

Scale: 4 / 8 / 16 / 24 / 32 / 48 / 64 / 96 / 128
Grid: 12-col, gutter 32 (wider than minimal — magazine breathing room)

## 4. Layout

- Container: max-w 760 (single-column reading) or 1200 (with sidebar)
- Breakpoints: sm 640 · md 768 · lg 1024 · xl 1280
- Drop-cap on opening paragraph: 4-line cap, accent color
- Pull-quote: serif italic, 28px, indented 48px, accent left rule

## 5. Components

- Button (primary): bg accent, fg cream, radius 4 (low), padding 12/20, serif weight 600
- Card: bg bg, no border, padding 32, hairline rule between cards
- Input: bg transparent, bottom-border 1px fg, no radius
- Pull-quote block: 32px serif italic, 48px left padding, 4px accent left rule

## 6. Motion

- Fast: 150ms ease
- Default: 250ms ease
- Slow: 400ms ease
- Page transitions: cross-fade preferred over slide

## 7. Voice

Tone keywords: considered · analytical · literary · understated
- Do say: full sentences, semicolons OK, em-dashes encouraged
- Don't say: emojis, exclamation marks, "you won't believe", clickbait

## 8. Brand

- Logo at masthead position (top-left or centered) — never wandering
- Clear-space ≥ 1.5× logo cap-height
- Don'ts: no glow, no rounded corners on logo, no neon recolor

## 9. Anti-patterns

- Sans-serif display (breaks the editorial frame)
- Card grids of equal-weight tiles (kills hierarchy — magazines have ONE hero per page)
- Bright accents (rainbow / neon)
- Glassmorphism / blur backgrounds
- Center-aligned long body text
