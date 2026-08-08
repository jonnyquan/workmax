# HTML-native deck template seed

Use this only when the user asks for an HTML companion, web preview, motion teaser, or exportable prototype alongside the editable `.pptx`.

## Contract
- Produce one self-contained `./outputs/{topic}-deck-preview.html`.
- Keep slide content synced with the `.pptx` outline; do not invent extra claims.
- Use semantic sections: `.deck`, `.slide`, `.kicker`, `.headline`, `.body`, `.visual`.
- Include `<meta name="viewport" content="width=device-width, initial-scale=1">`.
- Prefer CSS variables for design-system tokens: `--bg`, `--fg`, `--accent`, `--muted`, `--font-display`, `--font-body`.

## Layout
- Default canvas: 16:9, `aspect-ratio: 16 / 9`, max width `1280px`.
- Each slide must fit without text overflow at 1280x720 and 390x844.
- Use dense executive layouts, not landing-page hero sections.
- Keep charts as accessible HTML/CSS/SVG primitives with labels.

## Export Readiness
- Static exports: PNG/PDF.
- Motion exports: MP4/GIF only when transitions or timed reveals are explicitly requested.
- Avoid remote scripts, remote fonts, iframe embeds, and network-only assets.
