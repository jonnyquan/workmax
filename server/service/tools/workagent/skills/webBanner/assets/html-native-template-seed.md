# HTML-native web banner template seed

Use this when a display ad needs inspectable HTML, animation, or multi-format export beyond a flat image.

## Contract
- Produce one self-contained `./outputs/{campaign}-web-banner.html`.
- Match the requested IAB size exactly; common sizes: 728x90, 300x250, 160x600, 336x280, 300x600.
- Put the target size in a root data attribute: `<main class="banner" data-size="300x250">`.
- Include `<meta name="viewport" content="width=device-width, initial-scale=1">`.

## Structure
- `.banner`: fixed-format artboard with stable width/height.
- `.brand`: logo or brand mark zone.
- `.hook`: 3-7 word primary offer.
- `.proof`: one short support line.
- `.cta`: clear action button.

## Motion
- Keep total loop under 15 seconds.
- Use CSS keyframes only unless explicitly asked for JS.
- First frame must be meaningful as a static poster.
- Respect reduced motion with `@media (prefers-reduced-motion: reduce)`.

## Export Readiness
- Targets: HTML, PNG, GIF, MP4, ZIP.
- No remote scripts, iframes, external trackers, or layout that depends on network fonts.
