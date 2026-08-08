# HTML-native mobile story template seed

Use this when a 9:16 story needs storyboarding, motion preview, or exportable HTML/video variants.

## Contract
- Produce one self-contained `./outputs/{campaign}-mobile-story.html`.
- Root artboard: `aspect-ratio: 9 / 16`, 1080x1920 design intent, responsive down to 390x844.
- Include `<meta name="viewport" content="width=device-width, initial-scale=1">`.
- Keep all text inside top/bottom platform-safe zones.

## Structure
- `.story`: 9:16 artboard.
- `.scene`: one screen or timed beat.
- `.headline`: 3-8 word hook.
- `.visual`: product/person/scene anchor.
- `.caption`: optional support copy.
- `.cta`: final action or swipe hint.

## Motion
- Use a short timeline: 0-1s hook, 1-3s proof/demo, 3-5s CTA.
- Keep animation transform/opacity-based.
- Provide reduced-motion fallback that shows the final composed frame.

## Export Readiness
- Targets: HTML, PNG, MP4, GIF, ZIP.
- Avoid remote scripts, iframe embeds, autoplay audio, and network-only media.
