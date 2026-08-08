# Optional HTML motion helper

Use this for social motion previews and MP4/GIF export prototypes. Avoid external animation libraries.

## Minimal timeline pattern

```html
<script>
const timeline = [
  { selector: ".media", at: 0, duration: 700, from: { opacity: 0, scale: 1.04 }, to: { opacity: 1, scale: 1 } },
  { selector: ".hook", at: 180, duration: 450, from: { opacity: 0, y: 18 }, to: { opacity: 1, y: 0 } },
  { selector: ".cta", at: 1800, duration: 400, from: { opacity: 0, y: 10 }, to: { opacity: 1, y: 0 } }
];

function easeOutCubic(t) { return 1 - Math.pow(1 - t, 3); }
function applyFrame(item, progress) {
  const el = document.querySelector(item.selector);
  if (!el) return;
  const e = easeOutCubic(progress);
  const y = (item.from.y || 0) + ((item.to.y || 0) - (item.from.y || 0)) * e;
  const scale = (item.from.scale || 1) + ((item.to.scale || 1) - (item.from.scale || 1)) * e;
  el.style.opacity = (item.from.opacity + (item.to.opacity - item.from.opacity) * e).toFixed(3);
  el.style.transform = `translateY(${y.toFixed(1)}px) scale(${scale.toFixed(3)})`;
}
requestAnimationFrame(function tick(now) {
  for (const item of timeline) applyFrame(item, Math.max(0, Math.min(1, (now - item.at) / item.duration)));
  requestAnimationFrame(tick);
});
</script>
```

## Rules
- Design the 0 ms frame as a usable static ad.
- Keep the primary hook readable by 0.5 seconds.
- Add reduced-motion fallback and never autoplay audio.
