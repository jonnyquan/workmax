# Design System — Neutral Default

> Safe-but-not-bland baseline. Used as the universal fallback when
> the user hasn't picked a direction and no brand-spec is active.
> Inspired by Tailwind defaults / shadcn's neutral theme — modern
> but uncommitted to a strong personality.

`derived_from: neutral_default` (no fallback_5 entry — fallback when no other direction selected)

## 1. Color

| Slot | OKLch | Hex | Role |
|---|---|---|---|
| bg | oklch(98% 0 0) | #fafafa | near-white |
| fg | oklch(20% 0 0) | #2e2e2e | warm dark gray |
| accent | oklch(48% 0.18 240) | #1f4ec5 | safe blue |
| muted | oklch(75% 0 0) | #b5b5b5 | secondary |

Semantic:
- success: oklch(55% 0.15 145) / #2c8043
- warning: oklch(70% 0.16 75)  / #d99440
- error:   oklch(50% 0.18 25)  / #b53e30
- info:    accent

## 2. Typography

- Display: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif · weight 600 · sizes [40, 30, 24, 20]
- Body:    system-ui, -apple-system, "Segoe UI", Roboto, sans-serif · weight 400 / 500 · sizes [16, 14]
- Mono:    ui-monospace, SFMono-Regular, Menlo, monospace · weight 400 · size 13

Tracking: 0. Line height: 1.25 display, 1.5 body. System fonts only (no web-font load cost).

## 3. Spacing

Scale: 4 / 8 / 12 / 16 / 24 / 32 / 48 / 64 / 96
Grid: 12-col, gutter 24

## 4. Layout

- Container: max-w 1200, padding-x 24
- Breakpoints: sm 640 · md 768 · lg 1024 · xl 1280
- Vertical rhythm: 64px between major sections (one less than minimal — slightly tighter)

## 5. Components

- Button (primary): bg accent, fg white, radius 6, padding 8/14
- Button (secondary): 1px border muted, fg fg, radius 6
- Button (ghost): fg fg, hover bg muted/15
- Card: bg bg, 1px border muted/40, radius 8, padding 20
- Input: 1px border muted, radius 6, padding 8/12, focus: 2px ring accent

## 6. Motion

- Fast: 150ms ease-out
- Default: 200ms ease-out
- Slow: 300ms ease-out

## 7. Voice

Tone keywords: clear · neutral · helpful · professional
- Do say: simple direct sentences, "this", "the X"
- Don't say: hyperbolic adjectives, urgency phrases, jargon, emoji

## 8. Brand

- Logo on bg or accent only
- Clear-space ≥ 1× logo cap-height
- Don'ts: no glow, no animated logo, no gradient fill

## 9. Anti-patterns

- Inter font (too AI-default — use system-ui to feel platform-native)
- Bright/saturated accents (defeats the neutral baseline)
- Soft pastel palette (use soft-warm-lifestyle instead if that's wanted)
- Heavy display weights (>700 — looks brittle in system-ui)
- Stylized iconography (use plain Lucide / Heroicons defaults)
