# Design System — Vintage Film

> Wes Anderson / 1970s film stock / vintage tourism poster lineage.
> Warm color grade, slight halation, serif display, nostalgic
> framing. Best for character-driven and lifestyle work.

`derived_from: vintage_film` (visual-directions.yaml)

## 1. Color

| Slot | OKLch | Hex | Role |
|---|---|---|---|
| bg | oklch(96% 0.04 80) | #f6ecdb | cream-warm page |
| fg | oklch(25% 0.04 30) | #3e2c25 | warm ink |
| accent | oklch(60% 0.13 50) | #bf7a4d | tan — rules, bullets |
| muted | oklch(70% 0.03 60) | #b09a85 | secondary |

Semantic mapped to warmer hues:
- success: oklch(55% 0.10 110) / #79854b (olive)
- warning: oklch(65% 0.13 55)  / #c0844a
- error:   oklch(45% 0.16 25)  / #9c3d2c (oxide)

## 2. Typography

- Display: Cormorant Garamond, Tiempos, serif · weight 500 / 700 · sizes [54, 40, 30, 22]
- Body:    Tiempos Text, Georgia, serif · weight 400 / 500 · sizes [17, 15]
- Mono:    Courier Prime, Courier New, monospace · weight 400 · size 13

Italic body for pull-quotes is encouraged. Wide letter spacing on display (+0.02em).

## 3. Spacing

Scale: 4 / 8 / 16 / 24 / 32 / 48 / 64 / 96
Grid: 12-col, gutter 28

## 4. Layout

- Container: max-w 1080 (slightly narrower than minimal for compositional warmth)
- Breakpoints: sm 640 · md 768 · lg 1024 · xl 1280
- Asymmetric framing — rule-of-thirds favored for hero images
- Halation effect on hero photos (soft warm glow around highlights)

## 5. Components

- Button (primary): bg accent, fg bg, radius 2, padding 12/24, weight 600 serif
- Card: bg bg, hairline border accent at 30% opacity, radius 4, padding 24
- Input: underline-only border, no fill, fg color
- Photo frame: 4px ivory border + 8px cream margin (instant Polaroid feel)

## 6. Motion

- Fast: 180ms ease
- Default: 280ms ease (slightly slower — period-correct pacing)
- Slow: 480ms ease
- Page transitions: warm fade (cream tint during transition)

## 7. Voice

Tone keywords: nostalgic · warm · considered · slightly literary
- Do say: storytelling phrasing, "imagine a quiet morning"
- Don't say: corporate speak, urgency phrases, technical jargon

## 8. Brand

- Logo monogram preferred over wordmark
- Clear-space ≥ 2× logo cap-height (vintage breathing room)
- Don'ts: no neon recolor, no 3D bevel, no animated logo

## 9. Anti-patterns

- Cool blue / white sterile palette (kills the warmth)
- Sans-serif display (breaks period frame)
- Sharp-edged shadows (use halation / glow instead)
- Gradient backgrounds
- Modern emoji
