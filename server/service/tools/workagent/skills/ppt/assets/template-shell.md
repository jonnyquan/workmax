# PPT Template Shell

> Seed structure for ppt mode. The agent reads this BEFORE generating
> to anchor the deck's pacing — without a shell, every PPT starts from
> a blank slate and structural drift accumulates across mode invocations.
>
> Don't treat this as a hard contract. Skip / merge / reorder sections
> as the user's brief demands. The point is "start from a known-good
> skeleton", not "fill in the blanks".

## Section archetypes (5)

### 1. Cover
- Title slide: brand or topic, single bold statement, presenter / date
- Visual: hero image / color block / typography statement (one of)
- Anti-pattern: 6-card "intro grid" with avatars + role labels

### 2. Context (1–2 slides)
- Why this deck exists: the question / claim it answers
- Often a single-sentence framing slide + a "the world today" data point

### 3. Body (60% of total slide count)
- One idea per slide. If a slide has more than one claim, split.
- Mix: data slide, narrative slide, callout slide, visual break.
- Use the active design-system's palette tokens — never invent hex.
- Honest data rule: every metric quotes a source line at the bottom
  (10pt, secondary color). No source = `—` placeholder.

### 4. Synthesis (1 slide)
- The "so what" — what does the body lead the audience toward?
- Should be readable in <5 seconds.

### 5. CTA / Next steps
- One verb-led action item. "Review by Friday" / "Vote yes" / "Try the demo".
- Contact / Q&A reserved slide last (optional).

## Pacing rules

- Section 1 ≤ 1 slide
- Section 2 ≤ 2 slides
- Section 3 = 60% × user-requested slide count
- Section 4 ≤ 1 slide
- Section 5 ≤ 1–2 slides

If user requested 10 slides: 1 + 1 + 6 + 1 + 1 = 10.
If user requested 20: 1 + 2 + 12 + 1 + 4 (Q&A allowed).
