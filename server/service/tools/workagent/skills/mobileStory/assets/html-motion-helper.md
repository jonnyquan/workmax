# Optional HTML motion helper

Use this for 9:16 story previews and MP4/GIF export prototypes. It is a tiny timeline convention, not a required framework.

## Minimal timeline pattern

```html
<script>
const timeline = [
  { selector: ".scene-1 .headline", at: 0, duration: 500, from: { opacity: 0, y: 18 }, to: { opacity: 1, y: 0 } },
  { selector: ".scene-1 .visual", at: 250, duration: 900, from: { opacity: 0, scale: 1.06 }, to: { opacity: 1, scale: 1 } },
  { selector: ".scene-1 .cta", at: 3200, duration: 450, from: { opacity: 0, y: 14 }, to: { opacity: 1, y: 0 } }
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
- Keep story motion in 3-5 second beats.
- Use transform and opacity only.
- Add reduced-motion fallback showing the final composed frame.
