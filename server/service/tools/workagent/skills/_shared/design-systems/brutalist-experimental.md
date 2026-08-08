# Design System — Brutalist Experimental

> Achtung Mode / early-2010s independent design / OFFF posters
> lineage. Raw, oversized, no shadows, harsh accents. For
> experimental project moodboards and bold socialAd campaigns.

`derived_from: brutalist_experimental` (no fallback_5 entry — picker option for experimental projects)

## 1. Color

| Slot | OKLch | Hex | Role |
|---|---|---|---|
| bg | oklch(100% 0 0) | #ffffff | hard white |
| fg | oklch(0% 0 0) | #000000 | hard black |
| accent | oklch(70% 0.30 30) | #ff5824 | sharp red-orange |
| muted | oklch(40% 0 0) | #5c5c5c | secondary monochrome |

Optional alts (use only one per composition):
- alt-yellow: oklch(85% 0.20 95) / #ffce1f
- alt-magenta: oklch(60% 0.30 340) / #db2974

## 2. Typography

- Display: Helvetica Neue Bold, Arial Black, sans-serif · weight 900 · sizes [120, 72, 48, 32]
- Body:    Times New Roman, serif · weight 400 / 700 · sizes [16, 14]
- Mono:    Courier New, monospace · weight 700 · size 14

Tracking: 0 (or negative on display). Display can break out of frame, overlap, rotate. Body must respect grid.

## 3. Spacing

Scale: 0 / 4 / 8 / 16 / 32 / 64 / 128
Grid: chaotic — 6-col + free-form overlay layers

## 4. Layout

- Container: full-bleed often, max-w 1280 for body
- Breakpoints: sm 640 · md 768 · lg 1024 · xl 1280
- Hero: oversized type that breaks the page, may rotate ±2-15°
- Layers welcome — text on text, image on text, intentional clash

## 5. Components

- Button (primary): bg fg, fg bg, radius 0, padding 16/32, weight 900, no shadow, square corners
- Button (secondary): bg accent, fg fg, radius 0
- Card: solid color block (alt-* allowed), radius 0, padding 24, no border, no shadow
- Input: 2px solid fg bottom, no other borders, no radius
- Section: hard divider 8px solid fg

## 6. Motion

- Fast: 60ms (snap)
- Default: 120ms linear
- Slow: 200ms linear
- Easing: linear preferred — no soft easing
- Glitch / shift effects allowed for accent moments

## 7. Voice

Tone keywords: raw · declarative · provocative · anti-corporate
- Do say: ALL CAPS occasionally, fragmentary lines, manifestos
- Don't say: corporate hedging, "elegant", "refined", soft brand-speak

## 8. Brand

- Logo can be oversized, rotated, full-bleed
- Clear-space minimum (rules made to be broken intentionally here)
- Don'ts: no glow, no gradient, no soft shadow

## 9. Anti-patterns

- Pastel anything (this system is monochrome + 1 hot accent)
- Rounded corners (radius > 0 is wrong)
- Serif sans-serif mixing for display (pick monolithic display only)
- Multiple tertiary alts in one comp
- Drop shadows / glows
- "Tasteful" balance — this system rejects safe equilibrium
