# Design System — Soft Warm Lifestyle

> Notion marketing / Apple Health / wellness brand lineage. Generous,
> low-contrast, peachy neutrals, golden hour energy. Best for
> lifestyle, productShot, modelTryOn, flashCard.

`derived_from: soft_warm_lifestyle` (visual-directions.yaml)

## 1. Color

| Slot | OKLch | Hex | Role |
|---|---|---|---|
| bg | oklch(96% 0.03 60) | #f5e8d8 | peachy cream |
| fg | oklch(30% 0.03 30) | #473a31 | warm dark |
| accent | oklch(75% 0.10 40) | #daa687 | warm peach — primary CTA, links |
| muted | oklch(80% 0.02 60) | #cfc4b3 | secondary, hairlines |

Semantic (pastel-leaning):
- success: oklch(70% 0.10 130) / #98ad7a (sage)
- warning: oklch(75% 0.13 70)  / #e2b478
- error:   oklch(60% 0.16 25)  / #ce6f5a (warm coral, not red)

## 2. Typography

- Display: Migra, Cormorant Garamond, serif · weight 500 / 600 · sizes [44, 32, 26, 22]
- Body:    Inter Tight, system-ui, sans-serif · weight 400 / 500 · sizes [17, 15]
- Mono:    JetBrains Mono · weight 400 · size 13

Tracking: 0 display, 0.005em body. Line height: 1.25 display, 1.6 body.

## 3. Spacing

Scale: 8 / 12 / 16 / 24 / 32 / 48 / 64 / 96 / 128
Grid: 12-col, gutter 32

## 4. Layout

- Container: max-w 1120
- Breakpoints: sm 640 · md 768 · lg 1024 · xl 1280
- Hero: golden-hour lifestyle photo + serif headline overlay
- Generous padding — never tight, always breathing

## 5. Components

- Button (primary): bg accent, fg fg, radius 24 (pill), padding 12/24, weight 500
- Button (secondary): 1px border accent, fg accent, radius 24
- Card: bg bg, soft shadow (0 4 24 / fg-color at 6% alpha), radius 16, padding 28
- Input: bg muted/15, no border, radius 12, padding 12/16, focus: 2px ring accent

## 6. Motion

- Fast: 200ms ease
- Default: 320ms ease (slightly slower — calm pace)
- Slow: 480ms ease
- Easing: cubic-bezier(0.4, 0, 0.2, 1) — soft entry/exit

## 7. Voice

Tone keywords: warm · inviting · calm · personal
- Do say: second-person, conversational, "your", "we'll", soft questions
- Don't say: urgency, "act now", "limited time", aggressive caps

## 8. Brand

- Logo on bg or muted only
- Clear-space ≥ 1.5× logo cap-height
- Don'ts: no harsh outline, no monochrome on dark (loses warmth)

## 9. Anti-patterns

- Cool blue accents (kills warmth)
- Hard shadows / sharp corners
- Bold display weight (>600 — loses softness)
- High-contrast black text on cream (use warm dark fg instead)
- Sans-serif display (breaks the lifestyle frame)
