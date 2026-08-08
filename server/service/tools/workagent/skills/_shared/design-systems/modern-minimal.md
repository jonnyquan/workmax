# Design System — Modern Minimal

> Linear / Vercel / Stripe lineage. Cool, structured, restrained. The
> tokens here are the SOURCE OF TRUTH — never invent values, always
> quote from this file when emitting CSS / Tailwind / image prompts.

`derived_from: modern_minimal` (visual-directions.yaml)

## 1. Color

| Slot | OKLch | Hex | Role |
|---|---|---|---|
| bg | oklch(99% 0.005 0) | #fafafa | page background |
| fg | oklch(15% 0.005 0) | #1a1a1a | primary text |
| accent | oklch(50% 0.18 250) | #3151c4 | links, focus, primary button |
| muted | oklch(70% 0.005 0) | #a8a8a8 | secondary text, dividers |

Semantic:
- success: oklch(60% 0.15 145)  /  #2da34a
- warning: oklch(70% 0.15 75)   /  #d99440
- error:   oklch(55% 0.20 25)   /  #c64242
- info:    accent

## 2. Typography

- Display: Inter Display, system-ui, -apple-system, sans-serif · weight 600 / 700 · sizes [48, 36, 28, 22]
- Body:    Inter, system-ui, -apple-system, sans-serif · weight 400 / 500 · sizes [16, 14, 13]
- Mono:    JetBrains Mono, Menlo, monospace · weight 400 · size 13

Tracking: -0.02em on display, 0 on body. Line height: 1.2 display, 1.5 body, 1.4 mono.

## 3. Spacing

Scale: 4 / 8 / 12 / 16 / 24 / 32 / 48 / 64 / 96
Grid: 12-col, gutter 24, max-w 1200

## 4. Layout

- Container: max-w 1200, padding-x 24
- Breakpoints: sm 640 · md 768 · lg 1024 · xl 1280
- Section vertical rhythm: 96px between major sections
- Asymmetry over symmetry — favour off-grid hero arrangements

## 5. Components

- Button (primary): bg accent, fg white, radius 8, padding 10/16, weight 500
- Button (secondary): bg transparent, border 1px muted, fg fg, radius 8
- Button (ghost): no border, fg fg, hover bg muted/20
- Card: bg bg, border 1px muted/30, radius 12, padding 24, no shadow
- Input: border 1px muted, radius 8, padding 10/12, focus: 2px ring accent

## 6. Motion

- Fast: 120ms ease-out
- Default: 200ms ease-out
- Slow: 320ms ease-out
- Easing: cubic-bezier(0.16, 1, 0.3, 1) for entrances; ease-out for everything else

## 7. Voice

Tone keywords: clean · technical · understated · trustworthy
- Do say: "is", "ships", "supports", "produces"
- Don't say: "revolutionary", "game-changing", "10x", emoji decoration

## 8. Brand

- Logo on bg or accent only — never on muted
- Clear-space ≥ 1× logo height
- Don'ts: no drop-shadow, no gradient fill, no rotation, no outline variant

## 9. Anti-patterns

- Multiple accent colors competing
- Card with left-border accent stripe (AI-slop signature)
- Inter at sub-13px (illegible) or above 24px in body
- emoji 🚀 ✨ 💡 in headings or buttons
- Gradient backgrounds (mesh / rainbow / 3-color)
