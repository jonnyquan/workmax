# Character Pose Anchor Grid

> Reference for camera angles + pose archetypes. The agent picks ONE
> anchor per generation request and binds it via the prompt fragment.
> Multi-shot character cards use 3–9 anchors from this grid for
> consistency.

## 9 standard anchors

| Anchor | Camera | Distance | Purpose |
|--------|--------|----------|---------|
| `front_close` | front | head/shoulders | identity reference (the "ID photo" of the cast) |
| `front_full` | front | full body | outfit reference |
| `three_quarter_close` | 3/4 | head/shoulders | most flattering portrait angle |
| `three_quarter_full` | 3/4 | full body | walking / standing variation |
| `profile_close` | profile | head | silhouette / nose-bridge anchor |
| `profile_full` | profile | full body | gait / outfit drape |
| `back_three_quarter` | 3/4 from behind | full body | "walking away" narrative shot |
| `action_pose` | dynamic | full body | running / jumping / reaching |
| `seated_close` | front or 3/4 | seated | dialogue / interview framing |

## Locked features (must repeat across all anchors)

- Facial structure: eye distance, nose bridge, jawline shape
- Hair: color, length, parting, texture
- Skin tone descriptors
- One identifying mark (mole / scar / accessory) — pick ONE and lock

## Anti-patterns

- Generating 9 different faces in a 9-anchor card (failure mode = inconsistency)
- Using the same camera angle 9 times (failure mode = boring)
- Western-default face when user asked for Asian / specific ethnicity
