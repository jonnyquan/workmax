# Flash Card Template

> Front / back structure for flashCard mode. Density and complexity
> scale with the user's `age_group` answer.

## Front (recall side)

- ONE prompt: a question, term, image, or scenario
- Maximum word count by age group:
  - preschool (3–5): 1–3 words OR 1 image
  - elementary (6–10): 5–10 words
  - middle (11–14): 10–20 words
  - adult: 15–30 words

## Back (answer side)

- The answer, then a 1-line "why" or context
- Maximum word count by age group:
  - preschool: 5–10 words
  - elementary: 15–30 words
  - middle: 30–60 words
  - adult: 50–100 words

## Visual rules

- ≤ 2 fonts per card
- High contrast (text vs background ≥ AA 4.5:1)
- Subject-specific iconography ≤ 1 per side (not decoration)
- Color palette inherits from active design-system

## Spaced-repetition metadata (if requested)

- difficulty: 1–5
- recommended_interval_days: integer
- prerequisite_card_ids: optional list

## Anti-patterns

- Multiple questions on one front side
- Answer on the front (giveaway)
- Decorative emoji that competes with the actual content
- "Generic learning" copy ("learning is fun!" / "great job!")
