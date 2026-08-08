# HTML-native social ad template seed

Use this when the ad should be previewed as an interactive or motion-ready HTML artifact before exporting image/video variants.

## Contract
- Produce one self-contained `./outputs/{campaign}-social-ad.html`.
- Support platform-safe artboards: 1:1, 4:5, 9:16, and 1.91:1.
- Include `<meta name="viewport" content="width=device-width, initial-scale=1">`.
- Use CSS variables for design-system tokens and brand colors.

## Structure
- `.ad-frame`: fixed aspect-ratio artboard.
- `.media`: product/person/scene area.
- `.hook`: thumb-stopping headline.
- `.caption`: short supporting copy.
- `.cta`: platform-appropriate action.
- `.safe-zone`: non-rendered comment or CSS custom property documenting text-safe bounds.

## Motion
- For Reels/TikTok/Stories, design a 3-5 second first cut with clear first-frame readability.
- Use CSS keyframes or a tiny inline timeline object; avoid external libraries.
- Make the static first frame exportable as PNG.

## Export Readiness
- Targets: HTML, PNG, MP4, GIF, ZIP.
- No remote scripts, remote fonts, iframe embeds, or tracking pixels.
