# Optional HTML motion helper

Use this only when the banner requires animation or GIF/MP4 export. Static banners should not include JavaScript.

## Minimal timeline pattern

```html
<script>
const timeline = [
  { selector: ".hook", at: 0, duration: 450, from: { opacity: 0, y: 8 }, to: { opacity: 1, y: 0 } },
  { selector: ".cta", at: 650, duration: 350, from: { opacity: 0, scale: 0.96 }, to: { opacity: 1, scale: 1 } }
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
- The first frame must communicate the offer without waiting for animation.
- Keep loop duration under 15 seconds.
- Add `@media (prefers-reduced-motion: reduce)` with all elements visible.
