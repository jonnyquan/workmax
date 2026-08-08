# Optional HTML motion helper

Use this only for HTML companion previews or teaser exports. Do not use it for the editable `.pptx` source.

## Minimal timeline pattern

```html
<script>
const timeline = [
  { selector: ".slide-1 .headline", at: 0, duration: 500, from: { opacity: 0, y: 16 }, to: { opacity: 1, y: 0 } },
  { selector: ".slide-1 .visual", at: 250, duration: 700, from: { opacity: 0, scale: 0.98 }, to: { opacity: 1, scale: 1 } }
];

function applyFrame(item, progress) {
  const el = document.querySelector(item.selector);
  if (!el) return;
  const ease = 1 - Math.pow(1 - progress, 3);
  const opacity = item.from.opacity + (item.to.opacity - item.from.opacity) * ease;
  const y = (item.from.y || 0) + ((item.to.y || 0) - (item.from.y || 0)) * ease;
  const scale = (item.from.scale || 1) + ((item.to.scale || 1) - (item.from.scale || 1)) * ease;
  el.style.opacity = opacity.toFixed(3);
  el.style.transform = `translateY(${y.toFixed(1)}px) scale(${scale.toFixed(3)})`;
}
requestAnimationFrame(function tick(now) {
  for (const item of timeline) {
    const progress = Math.max(0, Math.min(1, (now - item.at) / item.duration));
    applyFrame(item, progress);
  }
  requestAnimationFrame(tick);
});
</script>
```

## Rules
- Prefer opacity and transform only.
- Keep total preview motion under 8 seconds for deck teasers.
- Provide a final static state for reduced-motion mode.
