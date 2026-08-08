# Design System — Bold Editorial

> Bloomberg Businessweek / Wired covers / Achtung Mode lineage.
> High-contrast, oversized typography, primary palette, hard light.
> Loud but considered — every element earns its size.

`derived_from: bold_editorial` (visual-directions.yaml)

## 1. Color

| Slot | OKLch | Hex | Role |
|---|---|---|---|
| bg | oklch(98% 0 0) | #fafafa | near-white |
| fg | oklch(10% 0 0) | #0a0a0a | near-black |
| accent | oklch(60% 0.25 30) | #e2492a | hot red — punctuation |
| muted | oklch(50% 0 0) | #7a7a7a | timestamps, byline |

Primary palette tertiaries (use sparingly, max 1 per composition):
- alt-blue:   oklch(50% 0.22 250) / #2645c2
- alt-yellow: oklch(80% 0.18 95)  / #e8b820
- alt-green:  oklch(55% 0.18 145) / #2d9550

## 2. Typography

- Display: Roslindale Display, Bricolage Grotesque, serif/sans hybrid · weight 800 · sizes [96, 64, 48, 36]
- Body:    Inter Tight, system-ui, sans-serif · weight 500 · sizes [17, 15]
- Mono:    JetBrains Mono · weight 600 · size 14

Display tracking: -0.04em (tight cluster). Display can break grid. Body must respect grid.

## 3. Spacing

Scale: 4 / 8 / 12 / 16 / 24 / 32 / 64 / 128 (note skip — discourages middling sizes)
Grid: 6-col with strong asymmetry (one column 3-wide, others 1)

## 4. Layout

- Container: max-w 1280 (let bold display breathe)
- Breakpoints: sm 640 · md 768 · lg 1024 · xl 1280
- Hero: oversize headline, 80%+ of fold height
- Body breaks display, headlines bleed off-screen if intentional

## 5. Components

- Button (primary): bg accent, fg bg, radius 0 (sharp), padding 14/28, weight 700, no shadow
- Button (secondary): 2px solid border accent, fg accent, radius 0
- Card: solid color block (alt-* allowed), radius 0, padding 32, bold heading
- Input: 2px border fg, no radius, focus: bg accent/10
- Section divider: 4px solid fg rule

## 6. Motion

- Fast: 100ms (snap)
- Default: 180ms
- Slow: 280ms
- Easing: linear or sharp ease-in — no soft bounces

## 7. Voice

Tone keywords: confident · direct · rhythm · provocative-but-clean
- Do say: short declaratives, statement sentences, headlines that command
- Don't say: hedging, soft caveats, "maybe", "could potentially"

## 8. Brand

- Logo at masthead in fg color, oversized OK
- Clear-space ≥ 1× logo cap-height
- Don'ts: no soft glow, no gradient logo fill, no 3D

## 9. Anti-patterns

- Soft pastels (kills the punch)
- Rounded everything (radius > 4 is wrong here)
- Drop shadows + blur (modern flat aesthetic, not editorial bold)
- Multiple alt-tertiary colors in one comp (max 1)
- Italic body (saves italic for emphasis only)
