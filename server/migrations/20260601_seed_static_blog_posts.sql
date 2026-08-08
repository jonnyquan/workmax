-- Seed 4 hand-curated long-form blog posts into w_blog (en + zh).
-- Idempotent: skips rows whose (slug, lang) already exists.
-- Source content: web/lib/data/static-blog-posts.ts (deleted in this branch).
--
-- Convention (from server/api/admin/admin_blog_api.go):
--   - en rows have main_blog_id = 0 and act as canonical
--   - zh translations point main_blog_id at the en row's id

-- Ensure the 'tutorials' category exists (slug used as category_code).
INSERT INTO `w_blog_category` (slug, title, lang, status, sort, main_category_id, created_at, updated_at)
SELECT 'tutorials', 'Tutorials', 'en', 1, 100, 0, NOW(), NOW()
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM `w_blog_category` WHERE slug = 'tutorials' AND lang = 'en');
INSERT INTO `w_blog_category` (slug, title, lang, status, sort, main_category_id, created_at, updated_at)
SELECT 'tutorials', '教程', 'zh', 1, 100, 0, NOW(), NOW()
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM `w_blog_category` WHERE slug = 'tutorials' AND lang = 'zh');

-- Insert EN rows first (canonical, main_blog_id = 0).
INSERT INTO `w_blog`
  (category_code, title, cover, seo_title, seo_keyword, seo_description, summary,
   slug, detail_content, sort, status, lang, main_blog_id, ai_record_id, created_at, updated_at)
SELECT 'tutorials',
       '5 Sora 2 prompts that hold character consistency',
       '',
       '5 Sora 2 prompts that hold character consistency',
       'Sora 2 prompts,Sora 2 character consistency,AI video character continuity,Sora 2 prompt examples,Sora 2 character lock,multi-shot Sora 2,WorkMax Sora 2 prompts',
       'Generate the same character twice and you usually get two different faces. These five prompt patterns — and the identity-anchor trick — keep Sora 2 on-model across shots.',
       'Generate the same character twice and you usually get two different faces. These five prompt patterns — and the identity-anchor trick — keep Sora 2 on-model across shots.',
       'sora-2-prompts-character-consistency',
       '<p>Sora 2 is the strongest text-to-video model available today for cinematic motion and longer continuous shots — and it\'s also one of the worst at remembering what your protagonist looks like across two consecutive renders. If you\'ve shipped any narrative video on it, you\'ve felt this: the lead\'s hair changes, the jacket shifts, the face drifts.</p>
<p>This post collects five prompt patterns that produce repeatable character looks on Sora 2, plus the identity-anchor trick that turns this from "guess the prompt" into "lock the character once."</p>
<h2>Why Sora 2 drifts</h2>
<p>Sora 2 was trained to generate visually rich, motion-consistent video clips — not to remember a <em>specific</em> person across calls. Each prompt is treated as an independent creative brief. If you describe "a young woman with red hair in a leather jacket" twice, you\'ll get a young woman with red hair in a leather jacket twice — but the women won\'t be the same person.</p>
<p>The fix is to give the model so much specificity that the variance space collapses to one face. Or, better: feed it an explicit identity reference instead of relying on text alone.</p>
<h2>Pattern 1: structured identity stack</h2>
<p>Replace single-line descriptions with a structured stack. Sora 2 obeys this format reliably:</p>
<pre><code class="language-">Subject: &lt;name&gt;, &lt;age range&gt;, &lt;ethnicity / heritage if relevant&gt;.
Distinctive features: &lt;3-4 specific features — eye colour, jawline,
  freckles, scar, hair length, hair texture, skin tone&gt;.
Wardrobe: &lt;jacket, top, bottom, footwear — with materials and colours&gt;.
Mood: &lt;emotional state — concerned, focused, joyful&gt;.

Action: &lt;what they do in this shot&gt;.</code></pre>
<p>The structure forces the prompt to carry consistent details every time. Save the Subject + Distinctive features + Wardrobe block as a reusable snippet; only the <strong>Action</strong> changes per shot.</p>
<h2>Pattern 2: anchor on rare features</h2>
<p>Common features ("brown hair", "blue eyes") leave too much variance space. Anchor on at least two features that are rarer in the training distribution: a left-side undercut, a chipped front tooth, a small mole below the right eye, gold-rimmed glasses, a healed scar across the cheekbone. Sora 2 is much better at preserving rare features across shots than common ones.</p>
<h2>Pattern 3: lock the wardrobe to materials</h2>
<p>"Black jacket" is loose. "Vintage cropped black leather biker jacket with silver hardware on a white tee" is tight — and Sora 2 will keep that jacket consistent across renders. Pick three garments and describe each in 6-10 words including material and one detail (zipper colour, pocket style, fit).</p>
<h2>Pattern 4: bind the framing language</h2>
<p>Sora 2 reads cinematography terms well. Bind your framing to a small, repeatable vocabulary:</p>
<ul>
<li><em>Medium close-up, eye-level, shallow depth of field</em></li>
<li><em>Wide tracking shot, low angle, long lens</em></li>
<li><em>Over-the-shoulder, handheld, natural light</em></li>
</ul>
<p>Reusing the same framing language stabilises the <em>visual</em> context around your character, which makes drift less noticeable even when it happens.</p>
<h2>Pattern 5: end the prompt with the action</h2>
<p>Sora 2 weights the end of the prompt slightly more heavily for motion. Put the <em>action</em> last — what the character does in this shot — and put the <em>identity</em> first. This protects identity from being overwritten by motion descriptors.</p>
<pre><code class="language-">[identity stack]
[wardrobe]
[framing]
[action]   ← last</code></pre>
<h2>The identity-anchor trick</h2>
<p>Five prompt patterns can take you a long way, but the cleaner fix is to stop relying on prompts alone. WorkMax\'s character library lets you save a character once — name, reference frame, identity description — and then bind that character to any shot. The platform automatically derives a 6-layer identity anchor (bone structure, features, marks, colour, skin, hair) and folds it into every Sora 2 prompt — and into Veo 3, Kling, and Seedance prompts too, when you switch models.</p>
<p>The win: instead of writing the identity stack into every prompt, you attach the character once and the anchors flow through automatically. The same character looks the same across episodes, across models, and across team members on the same project.</p>
<h2>What\'s next</h2>
<p>If consistency is the bottleneck, start with Pattern 1 (structured identity stack) and Pattern 2 (rare features). If you\'re shipping more than five shots, the manual approach gets expensive fast — that\'s the cue to use a character library.</p>
<p>→ <a href="/tools/video-generator">Open the WorkMax Video Generator</a> to try these prompts on Sora 2, Veo 3 and Kling side by side. → <a href="/use-cases/character-consistency">How character consistency works in WorkMax</a> → <a href="/features/sora-2">Sora 2 — model overview</a></p>',
       100, 1, 'en', 0, 0,
       '2026-05-07 00:00:00',
       '2026-05-07 00:00:00'
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM `w_blog` WHERE slug = 'sora-2-prompts-character-consistency' AND lang = 'en');

INSERT INTO `w_blog`
  (category_code, title, cover, seo_title, seo_keyword, seo_description, summary,
   slug, detail_content, sort, status, lang, main_blog_id, ai_record_id, created_at, updated_at)
SELECT 'tutorials',
       'How to write a Veo 3 prompt that actually works',
       '',
       'How to write a Veo 3 prompt that actually works',
       'Veo 3 prompt,Veo 3 prompt structure,Google Veo 3 prompt examples,Veo 3 audio prompt,Veo 3 cinematography,how to use Veo 3,WorkMax Veo 3 prompts',
       'Veo 3 ships with native audio and reads cinematography vocabulary fluently. Here\'s the prompt structure that gets the most out of both — without leaving silent frames or off-brief shots on the table.',
       'Veo 3 ships with native audio and reads cinematography vocabulary fluently. Here\'s the prompt structure that gets the most out of both — without leaving silent frames or off-brief shots on the table.',
       'veo-3-prompt-structure',
       '<p>Veo 3 has two superpowers most other text-to-video models don\'t: native synced audio (dialogue, ambience, sound effects) and a cinematography vocabulary that\'s accurate enough to direct from. Use both, and a single render covers the visual <em>and</em> the audio bed for an entire shot. Don\'t, and you\'re paying premium pricing for one-third of what the model can do.</p>
<p>Below is the prompt structure that gets the full instrument — and the four traps that quietly waste credits.</p>
<h2>The five-block structure</h2>
<p>Veo 3 reads structured prompts more reliably than freeform paragraphs. Use these five blocks, in this order:</p>
<pre><code class="language-">1. Setting — where, when, what&#x27;s around
2. Subject — who&#x27;s on screen, what they look like
3. Action — what&#x27;s happening
4. Cinematography — framing, camera move, lens, lighting
5. Audio — dialogue, ambience, sound effects, on-screen text</code></pre>
<p>The Audio block is the one most people skip. Don\'t.</p>
<h2>Setting</h2>
<p>One sentence. Where the shot happens, in what kind of space, in what light.</p>
<blockquote><p><em>A small, warmly-lit Tokyo ramen shop at 11pm in winter. Steam rises from the counter. Yellow paper lanterns hang outside.</em></p></blockquote>
<p>Avoid "establishing shot of" — let the cinematography block handle framing.</p>
<h2>Subject</h2>
<p>Who\'s on screen. Use the same identity-stack pattern Sora 2 responds to: name, age range, distinctive features, wardrobe.</p>
<blockquote><p><em>Subject: Aiko, late 20s, shoulder-length black hair tucked behind ears, narrow gold-rimmed glasses, charcoal grey wool coat over a cream sweater.</em></p></blockquote>
<p>If you\'re using WorkMax\'s character library, this block is auto-injected — you don\'t write it manually.</p>
<h2>Action</h2>
<p>What this person <em>does</em> in this shot. Keep it to one or two beats — Veo 3 clips are typically 8 seconds, so over-stuffing the action makes the model rush.</p>
<blockquote><p><em>Aiko sits down at the counter, lets out a slow breath, then picks up the menu and reads it carefully.</em></p></blockquote>
<h2>Cinematography</h2>
<p>This is where Veo 3 really earns the premium. It reads:</p>
<ul>
<li><strong>Framing</strong>: extreme close-up, close-up, medium close-up, medium, medium wide, wide, very wide</li>
<li><strong>Angles</strong>: eye-level, low angle, high angle, Dutch tilt, overhead</li>
<li><strong>Camera moves</strong>: locked off, slow push-in, pull-out, dolly left, tracking shot, handheld, gimbal stabilised</li>
<li><strong>Lenses / DOF</strong>: 35mm, 50mm, 85mm, anamorphic, shallow depth of field, deep depth of field</li>
<li><strong>Lighting</strong>: practical lights only, soft window light, hard sun, neon back-light, golden hour, blue hour</li>
</ul>
<blockquote><p><em>Cinematography: medium close-up, eye-level, slow gentle push-in over 8 seconds. 50mm lens, shallow depth of field. Practical light from the lanterns and the kitchen counter; warm yellow key on the face, cool blue rim from the street.</em></p></blockquote>
<p>The more specific you are here, the less the model improvises.</p>
<h2>Audio</h2>
<p>The block most prompts ignore. Veo 3 generates synced audio across three layers:</p>
<ul>
<li><strong>Ambience</strong>: the bed sound — what the location sounds like</li>
<li><strong>Dialogue</strong>: anything anyone says, written in quotes</li>
<li><strong>Effects</strong>: discrete sounds that map to actions in the shot</li>
</ul>
<blockquote><p><em>Audio: ambient quiet ramen shop — distant kitchen clatter, soft Japanese pop on a low radio, faint traffic muffled through the door. SFX: stool creaks as Aiko sits, pages of the menu rustle.</em></p></blockquote>
<p>If you don\'t want audio at all, write <code>Audio: none</code> — Veo 3 will generate a silent clip rather than improvising a track that fights with what you\'ll add later.</p>
<h2>Four traps that waste credits</h2>
<p><strong>1. Putting "cinematic" or "8K" in the prompt.</strong> These were useful with earlier models. Veo 3 ignores them; if anything, "cinematic" pushes it toward grey-flat colour grading you didn\'t ask for. Cut them.</p>
<p><strong>2. Stuffing two actions into one shot.</strong> Veo 3 clips are 8 seconds. Two beats is the budget — three feels rushed and the second beat gets clipped. Split into two clips and stitch in Canvas.</p>
<p><strong>3. Describing audio as part of the action block.</strong> "She sits down with a sigh" puts the sigh inside the visual, which Veo 3 may interpret as a facial expression instead of a sound. Move the sigh into the Audio block.</p>
<p><strong>4. Not setting the aspect ratio.</strong> Veo 3 defaults to 16:9. If you want 9:16 or 1:1, set it on the generator before submitting — re-rendering at a different aspect costs the same as a fresh render.</p>
<h2>Veo 3 vs Sora 2 — when to pick which</h2>
<ul>
<li><strong>Pick Veo 3</strong> when you need synced audio, on-screen text, or strict prompt obedience for stylised scenes.</li>
<li><strong>Pick Sora 2</strong> when you need longer continuous motion, complex physics, or 20-second clips.</li>
</ul>
<p>→ <a href="/vs/sora-2-vs-veo-3">Compare Sora 2 vs Veo 3 side by side</a> → <a href="/tools/video-generator">Open the Video Generator</a> → <a href="/features/veo-3">Veo 3 — model overview</a></p>',
       100, 1, 'en', 0, 0,
       '2026-05-07 00:00:00',
       '2026-05-07 00:00:00'
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM `w_blog` WHERE slug = 'veo-3-prompt-structure' AND lang = 'en');

INSERT INTO `w_blog`
  (category_code, title, cover, seo_title, seo_keyword, seo_description, summary,
   slug, detail_content, sort, status, lang, main_blog_id, ai_record_id, created_at, updated_at)
SELECT 'tutorials',
       'Kling image-to-video: when it beats Sora 2 (and when it doesn\'t)',
       '',
       'Kling image-to-video: when it beats Sora 2',
       'Kling image to video,Kling AI review,Kling vs Sora 2,Kling 2.6,image to video AI,Kuaishou Kling,best image to video,WorkMax Kling',
       'Kling is the fastest path from a still hero frame to a moving clip — and the strongest mainstream model on human motion. Here\'s where it pulls ahead, where it falls behind, and the exact workflow that makes it pay.',
       'Kling is the fastest path from a still hero frame to a moving clip — and the strongest mainstream model on human motion. Here\'s where it pulls ahead, where it falls behind, and the exact workflow that makes it pay.',
       'kling-image-to-video',
       '<p>If you\'re producing AI video at any volume, you\'ve probably tried the same shot through three or four models and watched the cost-per-clip climb fast. Sora 2 is the obvious flagship; Veo 3 brings audio. Kling sits in a different lane — it\'s the cheapest, fastest path from a still reference frame to a coherent moving clip, and on human motion it\'s currently ahead of everything else.</p>
<p>This post is about when that lane is the right one.</p>
<h2>What Kling is good at</h2>
<p>Three things, ranked by how much daylight Kling has on the rest of the field:</p>
<p><strong>1. Image-to-video.</strong> Drop a still frame, write a short motion prompt, and Kling produces a 10-second clip that respects the source frame\'s composition, lighting, and subject identity. Sora 2 and Veo 3 also do image-to-video, but Kling holds the source geometry tighter.</p>
<p><strong>2. Human motion.</strong> Dance, sport, walking, gesturing, facial performance — Kling renders human motion with fewer "AI tells" (the bobbing, sliding feet, melting hands) than competitors. If your shot has a person doing a recognisable action, Kling is usually the safer bet.</p>
<p><strong>3. Iteration speed.</strong> A Kling render lands in well under a minute; Sora 2 can take 5x longer for a 20-second cinematic clip. If you\'re testing 10 prompt variants on the same hero frame, the time-to-pick-a-winner advantage compounds fast.</p>
<h2>What it isn\'t good at</h2>
<p><strong>Long-form cinematic motion.</strong> Kling clips are 10 seconds today. For a 20-second one-take cinematic shot, Sora 2 still wins.</p>
<p><strong>Native audio.</strong> Kling doesn\'t generate audio. Add it via TTS or licensed library after the render.</p>
<p><strong>Stylised art directions.</strong> Compared to Midjourney V7 fed into Kling, going prompt-only on Kling produces visually competent but less distinctive renders. Pair Kling with a strong hero frame for look-dev.</p>
<h2>The workflow that makes Kling pay</h2>
<p>The mistake people make: prompting Kling like Sora 2, end-to-end from text. Kling rewards a different shape — <em>image first, motion second</em>.</p>
<pre><code class="language-">1. Generate the hero frame in Nano Banana Pro / GPT Image 2 / Midjourney
2. Lock the look — composition, lighting, subject identity
3. Send the frame + a short motion prompt to Kling
4. Iterate fast — 3-5 motion variants on the same frame
5. Pick the winner; stitch into Canvas if you need a multi-shot scene</code></pre>
<p>Step 3\'s motion prompt is the load-bearing one. Two rules:</p>
<ul>
<li><strong>Be specific about the motion type, not the visual.</strong> "Slow handheld push-in over 8 seconds" beats "cinematic." The visual is already locked in the source frame.</li>
<li><strong>Keep it to one beat.</strong> "She turns her head and smiles" is two beats — Kling will rush. Pick one.</li>
</ul>
<h2>Where Kling beats Sora 2 head-to-head</h2>
<p>Three concrete cases, with the rough rule of thumb:</p>
<p><strong>Product hero shots from a still frame.</strong> Kling preserves product geometry better — text-to-video models often distort logos and proportions. Win for Kling.</p>
<p><strong>Talking-head or presenter clips with no audio.</strong> Kling renders facial micro-motion (blinks, mouth shape, micro-head-turns) that reads as natural in 8-10 second loops. If you\'re going to add audio later anyway, save the cost on Veo 3.</p>
<p><strong>Dance, sport, action with recognisable form.</strong> Sora 2 and Veo 3 both still struggle with limbs in motion at the level of detail Kling reaches.</p>
<h2>Where Sora 2 wins</h2>
<p><strong>Long takes and complex camera moves.</strong> Sora 2 plays comfortably at 20 seconds with continuous motion; Kling is 10 seconds and tighter on camera complexity.</p>
<p><strong>Heavy physics shots.</strong> Cloth, water, smoke, glass — Sora 2 leads.</p>
<p><strong>Anything that needs synced audio.</strong> Use Veo 3 instead.</p>
<h2>Practical: which model per shot</h2>
<p>A rule that works in production: <em>Kling for body &amp; product, Sora 2 for cinema, Veo 3 for talk</em>.</p>
<p>In WorkMax, this maps to a single workflow — write the brief once, route per shot to the right model, and the platform keeps your character / brand / asset library consistent across all three.</p>
<p>→ <a href="/features/kling">Kling — model overview</a> → <a href="/vs/kling-2-6-vs-sora-2">Compare Kling 2.6 vs Sora 2</a> → <a href="/tools/video-generator">Open the Video Generator</a></p>',
       100, 1, 'en', 0, 0,
       '2026-05-07 00:00:00',
       '2026-05-07 00:00:00'
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM `w_blog` WHERE slug = 'kling-image-to-video' AND lang = 'en');

INSERT INTO `w_blog`
  (category_code, title, cover, seo_title, seo_keyword, seo_description, summary,
   slug, detail_content, sort, status, lang, main_blog_id, ai_record_id, created_at, updated_at)
SELECT 'tutorials',
       'Nano Banana Pro vs GPT Image 2: practical differences',
       '',
       'Nano Banana Pro vs GPT Image 2: practical differences',
       'Nano Banana Pro vs GPT Image 2,Nano Banana Pro review,GPT Image 2 review,best AI image model,AI image model comparison,Google Nano Banana,OpenAI GPT Image,WorkMax image model',
       'Two flagship image models, two distinct strengths. Nano Banana Pro wins on photorealism and character consistency; GPT Image 2 wins on on-screen text and instruction following. Here\'s how to pick per shot.',
       'Two flagship image models, two distinct strengths. Nano Banana Pro wins on photorealism and character consistency; GPT Image 2 wins on on-screen text and instruction following. Here\'s how to pick per shot.',
       'nano-banana-pro-vs-gpt-image-2',
       '<p>Both Nano Banana Pro and GPT Image 2 are at the top of the image model landscape. They feel different in production, and the right one depends on the job — not on which provider has the more recent press cycle.</p>
<p>This post draws the line cleanly so you can pick per shot rather than per subscription.</p>
<h2>Where Nano Banana Pro pulls ahead</h2>
<p><strong>Photorealism.</strong> Nano Banana Pro renders skin, hair, fabric, and incidental detail (chip in a teacup, scuff on a shoe, wear on a wooden table) with fewer obvious AI artifacts. If the brief is "this should look like a real photo," it\'s the safer first try.</p>
<p><strong>Character consistency across renders.</strong> Pro is currently the strongest mainstream image model at preserving a character\'s face and body across multiple renders when given a reference frame. For brand spokespersons, IP characters, and recurring product hero subjects, that consistency is the load-bearing feature.</p>
<p><strong>4K headroom.</strong> Pro is comfortable at 4K output. GPT Image 2\'s native max is lower; you\'ll lean on an upscaler for the same final asset.</p>
<p><strong>Vertex AI integration.</strong> If your pipeline lives in Google Cloud, Pro is one click away.</p>
<h2>Where GPT Image 2 pulls ahead</h2>
<p><strong>On-screen text.</strong> GPT Image 2 still leads on legible, well-kerned, on-image text — product labels, ad copy, sign-painting, cards, posters with typographic hierarchy. Nano Banana Pro has improved here but isn\'t quite at parity.</p>
<p><strong>Instruction following on complex briefs.</strong> When a prompt has 6+ constraints (e.g. "three people, the leftmost holds a clipboard, the middle one wears a green hat, the right one points off-screen, in a 1990s convenience store, neon-lit, low angle"), GPT Image 2 follows more of them more accurately.</p>
<p><strong>ChatGPT integration.</strong> If you\'re already prompting in ChatGPT, the round-trip is shorter.</p>
<p><strong>Permissive style range.</strong> GPT Image 2 covers a slightly wider range of stylised aesthetics out of the box without prompt acrobatics.</p>
<h2>Where they tie</h2>
<p>Both ship <strong>strong colour control</strong>, <strong>decent compositional intelligence</strong>, and <strong>safe content policies</strong>. Both are closed-weight; if you need self-hosting, neither is your model — pick Stable Diffusion XL.</p>
<p>Both are <strong>expensive per render</strong> at the highest quality setting. Use the cheaper model for ideation and the flagship for the final.</p>
<h2>Practical: which model per shot</h2>
<p>A 4-row decision table that works in production:</p>
<table><thead><tr>
<th>Shot type</th>
<th>Pick</th>
</tr></thead><tbody>
<tr>
<td>Brand hero with a person, photoreal</td>
<td>Nano Banana Pro</td>
</tr>
<tr>
<td>Product packshot</td>
<td>Nano Banana Pro</td>
</tr>
<tr>
<td>Poster / OOH / on-image headline</td>
<td>GPT Image 2</td>
</tr>
<tr>
<td>Editorial illustration / stylised</td>
<td>GPT Image 2 (or Midjourney V7 if you have it)</td>
</tr>
<tr>
<td>Recurring character across episodes</td>
<td>Nano Banana Pro + WorkMax character library</td>
</tr>
<tr>
<td>Ad creative with copy on the image</td>
<td>GPT Image 2</td>
</tr>
<tr>
<td>Reference frame for video (→ Kling / Sora 2)</td>
<td>Nano Banana Pro</td>
</tr>
</tbody></table>
<p>If you\'re producing recurring characters, the character library multiplier on top of Pro makes the win-margin much bigger than the model alone — the same character looks the same across every Pro render <em>and</em> every video render that uses Pro frames as references.</p>
<h2>The variant tactic</h2>
<p>For high-stakes hero frames, render the same prompt through both models in parallel, pick the winner per criterion (lighting, geometry, text, character likeness). The cost of two renders is small compared to a re-shoot.</p>
<p>In WorkMax this is one click — set "fan-out" on the prompt and the same input goes to both Pro and GPT Image 2; both come back as siblings on the same canvas card.</p>
<h2>What to do next</h2>
<p>If you\'re picking a default for daily look-dev: <strong>Nano Banana Pro</strong>. If you\'re shipping a hero with on-image text: <strong>GPT Image 2</strong>. If you don\'t know yet: <strong>fan-out and decide on output</strong>.</p>
<p>→ <a href="/vs/gpt-image-2-vs-nano-banana-pro">Compare Nano Banana Pro vs GPT Image 2</a> → <a href="/features/nano-banana-pro">Nano Banana Pro — model overview</a> → <a href="/tools/image-generator">Open the Image Generator</a></p>',
       100, 1, 'en', 0, 0,
       '2026-05-07 00:00:00',
       '2026-05-07 00:00:00'
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM `w_blog` WHERE slug = 'nano-banana-pro-vs-gpt-image-2' AND lang = 'en');

-- Insert ZH translations, pointing main_blog_id at the en row.
INSERT INTO `w_blog`
  (category_code, title, cover, seo_title, seo_keyword, seo_description, summary,
   slug, detail_content, sort, status, lang, main_blog_id, ai_record_id, created_at, updated_at)
SELECT 'tutorials',
       '5 个能锁住角色一致性的 Sora 2 prompt 模板',
       '',
       '5 个能锁住角色一致性的 Sora 2 prompt 模板',
       'Sora 2 prompts,Sora 2 character consistency,AI video character continuity,Sora 2 prompt examples,Sora 2 character lock,multi-shot Sora 2,WorkMax Sora 2 prompts',
       '用 Sora 2 连续生成同一个角色，通常会得到两张不同的脸。这五个 prompt 模板，加上身份锚点技巧，能让 Sora 2 跨镜头保持设定。',
       '用 Sora 2 连续生成同一个角色，通常会得到两张不同的脸。这五个 prompt 模板，加上身份锚点技巧，能让 Sora 2 跨镜头保持设定。',
       'sora-2-prompts-character-consistency',
       '<p>Sora 2 是今天可用的文生视频模型里，电影感运动和长镜头表现最强的一档 —— 同时也是「记不住你主角长什么样」的代表。任何在它上面出过叙事内容的人都体会过：主角的发型变了、外套不一样了、脸还漂了。</p>
<p>本文整理 5 个能在 Sora 2 上稳定复现角色的 prompt 模板，外加「身份锚点」这一招，把它从「猜 prompt」升级为「角色锁一次就行」。</p>
<h2>Sora 2 为什么会漂</h2>
<p>Sora 2 的训练目标是输出画面丰富、运动一致的视频片段 —— 而不是跨多次调用记住某个具体的人。每个 prompt 在它眼里都是一个独立的创意 brief。如果你两次描述「红发皮夹克的年轻女性」，你会拿到两个红发皮夹克年轻女性 —— 但她们不是同一个人。</p>
<p>解法是把描述写到具体度足够把方差空间压成一张脸；或者更好：直接给一个显式的身份引用，不再纯靠文字。</p>
<h2>模板 1：结构化身份栈</h2>
<p>把单行描述换成结构化堆叠。Sora 2 对这种格式服从度更稳：</p>
<pre><code class="language-">Subject: 名字，年龄段，必要的族裔/出身。
Distinctive features: 3–4 个具体特征 — 眼色、下颌线、雀斑、伤疤、
  发长、发质、肤色。
Wardrobe: 外套、上身、下身、鞋 — 含材质与颜色。
Mood: 情绪状态 — 担忧、专注、愉悦。

Action: 这个镜头里他/她做什么。</code></pre>
<p>结构强制 prompt 每次都带上一致的细节。把 Subject + Distinctive features + Wardrobe 这一块保存成可复用片段，每个镜头只改 <strong>Action</strong>。</p>
<h2>模板 2：锚定罕见特征</h2>
<p>常见特征（「棕发」、「蓝眼」）方差空间太大。至少锁定两个在训练分布里更罕见的特征：左侧寸头、缺一颗门牙、右眼下方一颗小痣、金边眼镜、颧骨上一道愈合伤疤。Sora 2 跨镜头保留罕见特征的能力比保留常见特征强得多。</p>
<h2>模板 3：把服装锁到材质</h2>
<p>「黑色外套」太松。「vintage 短款黑色皮夹克，银色五金，内搭白 T」就紧 —— Sora 2 能跨渲染保持这件外套。挑三件衣物，每件用 6–10 个词描述（含材质 + 一个细节，比如拉链颜色、口袋样式、版型）。</p>
<h2>模板 4：绑定运镜词汇</h2>
<p>Sora 2 对摄影术语理解准确。把镜头语言绑定到一个小而重复的词表：</p>
<ul>
<li><em>Medium close-up, eye-level, shallow depth of field</em></li>
<li><em>Wide tracking shot, low angle, long lens</em></li>
<li><em>Over-the-shoulder, handheld, natural light</em></li>
</ul>
<p>复用同一套运镜词汇能稳定角色周围的<em>视觉</em>上下文 —— 即便偶尔漂了，也不那么明显。</p>
<h2>模板 5：把动作放在 prompt 末尾</h2>
<p>Sora 2 对 prompt 末尾的运动描述权重略高。把<em>动作</em>放最后 — 这个镜头里角色做什么 — 把<em>身份</em>放前面。这样身份不会被运动描述覆盖。</p>
<pre><code class="language-">[身份栈]
[服装]
[运镜]
[动作]   ← 最后</code></pre>
<h2>身份锚点这一招</h2>
<p>5 个 prompt 模板能带你走一段，但更干净的修法是：别再纯靠 prompt。WorkMax 角色库让你把角色保存一次 — 名字、参考帧、身份描述 — 然后把这个角色绑定到任意镜头。平台自动派生 6 层身份锚点（骨架、五官、特征、色调、肤色、发型），并把它们注入每一次 Sora 2 prompt — 切到 Veo 3、Kling、Seedance 时也是同一套。</p>
<p>收益：不必每个 prompt 都把身份栈再写一遍 — 角色绑一次，锚点跨镜头、跨模型、跨团队成员自动流转。</p>
<h2>下一步</h2>
<p>如果一致性是瓶颈，从模板 1（结构化身份栈）和模板 2（罕见特征）开始。如果要出 5 个以上镜头，手工方法的成本会很快上升 —— 这时候就该用角色库。</p>
<p>→ <a href="/tools/video-generator">打开 WorkMax 视频生成器</a>，把这些 prompt 在 Sora 2、Veo 3、Kling 之间并排对比。 → <a href="/use-cases/character-consistency">WorkMax 怎么实现角色一致性</a> → <a href="/features/sora-2">Sora 2 — 模型介绍</a></p>',
       100, 1, 'zh',
       (SELECT id FROM (SELECT id FROM `w_blog` WHERE slug = 'sora-2-prompts-character-consistency' AND lang = 'en' LIMIT 1) AS en_row), 0,
       '2026-05-07 00:00:00',
       '2026-05-07 00:00:00'
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM `w_blog` WHERE slug = 'sora-2-prompts-character-consistency' AND lang = 'zh');

INSERT INTO `w_blog`
  (category_code, title, cover, seo_title, seo_keyword, seo_description, summary,
   slug, detail_content, sort, status, lang, main_blog_id, ai_record_id, created_at, updated_at)
SELECT 'tutorials',
       '怎么写一个真正能跑的 Veo 3 prompt',
       '',
       '怎么写一个真正能跑的 Veo 3 prompt',
       'Veo 3 prompt,Veo 3 prompt structure,Google Veo 3 prompt examples,Veo 3 audio prompt,Veo 3 cinematography,how to use Veo 3,WorkMax Veo 3 prompts',
       'Veo 3 自带原生音频，且能流利理解运镜术语。下面这个 prompt 结构能把这两点的潜力都榨出来 — 不留沉默画面，也不偏离 brief。',
       'Veo 3 自带原生音频，且能流利理解运镜术语。下面这个 prompt 结构能把这两点的潜力都榨出来 — 不留沉默画面，也不偏离 brief。',
       'veo-3-prompt-structure',
       '<p>Veo 3 有两个大多数文生视频模型没有的超能力：原生同步音频（对白、环境音、音效）+ 准确到能用来「布镜」的运镜词汇。两个都用上，一次渲染就能覆盖一个镜头的画面<em>与</em>音轨。不用，等于付着旗舰价钱只用了三分之一。</p>
<p>下面是把整套乐器都奏起来的 prompt 结构 + 四个会悄悄烧额度的陷阱。</p>
<h2>五段式结构</h2>
<p>Veo 3 对结构化 prompt 比对自由散文更稳。按下面顺序写五段：</p>
<pre><code class="language-">1. Setting    — 何时何地，周围有什么
2. Subject    — 谁在画面里，长什么样
3. Action     — 在做什么
4. Cinematography — 取景、运镜、镜头、光线
5. Audio      — 对白、环境音、音效、屏幕文字</code></pre>
<p>最常被跳过的是 Audio 段。别跳。</p>
<h2>Setting</h2>
<p>一句话。镜头发生在哪儿、在什么样的空间、什么光线下。</p>
<blockquote><p><em>深冬晚 11 点，东京一家小而暖光的拉面店。柜台上方升起蒸汽，门外挂着黄色纸灯笼。</em></p></blockquote>
<p>避免写「establishing shot of」 — 镜头语言交给 Cinematography 段。</p>
<h2>Subject</h2>
<p>画面里的人。和 Sora 2 一样的身份栈：名字、年龄段、显著特征、服装。</p>
<blockquote><p><em>Subject: Aiko，二十多岁后期，齐肩黑发别在耳后，窄金边眼镜，炭灰羊毛大衣内搭奶白毛衣。</em></p></blockquote>
<p>如果用 WorkMax 角色库，这段会被自动注入 — 你不需要手写。</p>
<h2>Action</h2>
<p>这个人<em>在这个镜头里</em>做什么。一两个 beat 就够 — Veo 3 单镜头一般 8 秒，塞太多动作会让模型抢节奏。</p>
<blockquote><p><em>Aiko 在柜台坐下，缓缓吐了一口气，然后拿起菜单仔细看。</em></p></blockquote>
<h2>Cinematography</h2>
<p>这一段是 Veo 3 真正值这个价的地方。它认得：</p>
<ul>
<li><strong>取景</strong>：特写、近景、中近景、中景、中远景、远景、大远景</li>
<li><strong>机位</strong>：平视、仰拍、俯拍、Dutch tilt、顶拍</li>
<li><strong>运镜</strong>：定机位、缓推、缓拉、左移、跟镜、手持、稳定器</li>
<li><strong>镜头/景深</strong>：35mm、50mm、85mm、变形宽银幕、浅景深、深景深</li>
<li><strong>光线</strong>：纯实用光、柔和窗光、硬太阳、霓虹背光、黄金时刻、蓝色时刻</li>
</ul>
<blockquote><p><em>Cinematography: 中近景，平视，8 秒缓慢推近。50mm 镜头，浅景深。光源仅来自灯笼和厨房柜台 — 暖黄主光打脸，街道冷蓝从背后勾轮廓。</em></p></blockquote>
<p>写得越具体，模型自由发挥的空间越小。</p>
<h2>Audio</h2>
<p>最常被忽略的一段。Veo 3 在三层上生成同步音频：</p>
<ul>
<li><strong>Ambience</strong>：背景床声 — 这个空间听起来是什么样</li>
<li><strong>Dialogue</strong>：有人说的台词，用引号包住</li>
<li><strong>Effects</strong>：与镜头中动作对应的离散音</li>
</ul>
<blockquote><p><em>Audio: 安静的拉面店环境 — 远处厨房叮当声、低音量日本流行电台、门外微弱的交通声。SFX: 椅子在 Aiko 坐下时轻响，菜单翻页声。</em></p></blockquote>
<p>如果完全不需要音频，写 <code>Audio: none</code> — Veo 3 会输出静默版本，而不是自作主张配一条会跟你后期音轨打架的音频。</p>
<h2>四个会烧额度的陷阱</h2>
<p><strong>1. 在 prompt 里写「cinematic」或「8K」。</strong> 这些是早期模型时代的用法。Veo 3 直接忽略；甚至「cinematic」会把它推向你没要的低饱和灰平调色。删掉。</p>
<p><strong>2. 一个镜头塞两个动作。</strong> Veo 3 单镜 8 秒。两个 beat 是预算上限；三个会被赶节奏，第二个被切掉。分两个镜头，用 Canvas 串。</p>
<p><strong>3. 把声音写进 Action 段。</strong> 「她叹了口气坐下」会被解读成画面里的表情而不是声音。把叹息挪到 Audio 段。</p>
<p><strong>4. 没设画幅。</strong> Veo 3 默认 16:9。要 9:16 或 1:1 在提交前在生成器里设 — 不同画幅重渲与全新渲染同价。</p>
<h2>Veo 3 vs Sora 2 — 该选哪个</h2>
<ul>
<li><strong>选 Veo 3</strong>：需要同步音频、屏幕文字、风格化场景下严格 prompt 服从的时候。</li>
<li><strong>选 Sora 2</strong>：需要更长连续运动、复杂物理、20 秒镜头的时候。</li>
</ul>
<p>→ <a href="/vs/sora-2-vs-veo-3">Sora 2 vs Veo 3 并排对比</a> → <a href="/tools/video-generator">打开视频生成器</a> → <a href="/features/veo-3">Veo 3 — 模型介绍</a></p>',
       100, 1, 'zh',
       (SELECT id FROM (SELECT id FROM `w_blog` WHERE slug = 'veo-3-prompt-structure' AND lang = 'en' LIMIT 1) AS en_row), 0,
       '2026-05-07 00:00:00',
       '2026-05-07 00:00:00'
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM `w_blog` WHERE slug = 'veo-3-prompt-structure' AND lang = 'zh');

INSERT INTO `w_blog`
  (category_code, title, cover, seo_title, seo_keyword, seo_description, summary,
   slug, detail_content, sort, status, lang, main_blog_id, ai_record_id, created_at, updated_at)
SELECT 'tutorials',
       'Kling 图生视频：什么场景比 Sora 2 强（什么场景不如）',
       '',
       'Kling 图生视频：什么场景比 Sora 2 强',
       'Kling image to video,Kling AI review,Kling vs Sora 2,Kling 2.6,image to video AI,Kuaishou Kling,best image to video,WorkMax Kling',
       'Kling 是从一张 hero 帧走到视频片段最快的路径，也是主流模型里人物运动最强的一档。下面是它领先的场景、落后的场景，以及让它真正划得来的工作流。',
       'Kling 是从一张 hero 帧走到视频片段最快的路径，也是主流模型里人物运动最强的一档。下面是它领先的场景、落后的场景，以及让它真正划得来的工作流。',
       'kling-image-to-video',
       '<p>如果你在以一定规模出 AI 视频，你大概率试过把同一个镜头送到三四个模型，看着每镜成本很快上升。Sora 2 是显然的旗舰；Veo 3 带音频。Kling 在另一条赛道 — 从一张参考帧走到连贯片段最便宜、最快，且在人物运动上目前领先全场。</p>
<p>这篇说的是这条赛道什么时候该走。</p>
<h2>Kling 强在哪</h2>
<p>三件事，按和其他模型的差距从大到小排：</p>
<p><strong>1. 图生视频。</strong> 丢一张帧，写一句运动 prompt，Kling 给你一段 10 秒的视频，保留源帧的构图、光线、主体身份。Sora 2 和 Veo 3 也支持图生视频，但 Kling 对源几何的保持更紧。</p>
<p><strong>2. 人物运动。</strong> 跳舞、运动、走路、比划、面部表演 — Kling 在「AI 痕迹」（脚下漂、手化掉、关节奇怪）上比对手少得多。镜头里如果有人在做某个能认出来的动作，Kling 通常更稳。</p>
<p><strong>3. 迭代速度。</strong> Kling 单次渲染在一分钟内回；Sora 2 出一段 20 秒电影感镜头能多花 5 倍时间。同一张 hero 帧上跑 10 个 prompt 变体的时候，"挑出优胜版本所花时间"差距会快速拉大。</p>
<h2>它不擅长什么</h2>
<p><strong>长镜头电影感运动。</strong> Kling 单镜目前 10 秒。20 秒一镜到底的电影感镜头，Sora 2 仍然更稳。</p>
<p><strong>原生音频。</strong> Kling 不生成音频。渲染后用 TTS 或音效库补上。</p>
<p><strong>风格化美术。</strong> 同样是 prompt 直出，Kling 的视觉竞争力不如 Midjourney V7 → Kling 的图生视频路线。把 Kling 和一张强 hero 帧搭配做 look-dev。</p>
<h2>让 Kling 真正划得来的工作流</h2>
<p>常见错误：把 Kling 当 Sora 2 用，端到端纯文本 prompt。Kling 喜欢的形状不一样 — <em>图先行，动作后行</em>。</p>
<pre><code class="language-">1. 在 Nano Banana Pro / GPT Image 2 / Midjourney 里出 hero 帧
2. 锁定构图、光线、主体身份
3. 把帧 + 一句简短的运动 prompt 送进 Kling
4. 在同一张帧上跑 3-5 个运动变体
5. 选优胜版本；如果需要多镜头场景，串到 Canvas 里</code></pre>
<p>第 3 步的运动 prompt 是关键。两个规则：</p>
<ul>
<li><strong>具体描述动作类型，而不是视觉。</strong> "Slow handheld push-in over 8 seconds" 强过 "cinematic"。视觉已经锁在源帧里了。</li>
<li><strong>只写一个 beat。</strong> "她转头然后微笑" 是两个 beat — Kling 会赶节奏。挑一个。</li>
</ul>
<h2>Kling 头对头打赢 Sora 2 的三种情况</h2>
<p>具体场景 + 经验法则：</p>
<p><strong>从静帧出商品 hero 镜头。</strong> Kling 对商品几何的保留更好 — 文生视频常会把 Logo 和比例做偏。Kling 赢。</p>
<p><strong>没音频的讲话头/主持人短镜。</strong> Kling 在 8-10 秒回环里面部微动作（眨眼、口型、微转头）的自然度更高。反正后期要补音轨，省下 Veo 3 那笔。</p>
<p><strong>跳舞、运动、能识别动作形态的场景。</strong> Sora 2 和 Veo 3 都还在挣扎运动中的肢体细节，Kling 已经过关。</p>
<h2>Sora 2 仍然赢的场景</h2>
<p><strong>长镜头和复杂运镜。</strong> Sora 2 跑到 20 秒带连贯运动也稳；Kling 10 秒，且对镜头复杂度更敏感。</p>
<p><strong>重物理。</strong> 布料、水、烟、玻璃 — Sora 2 领先。</p>
<p><strong>任何需要同步音频的场景。</strong> 选 Veo 3。</p>
<h2>实操：每个镜头选哪个模型</h2>
<p>生产里好用的一句话：<em>Kling 拍人和物，Sora 2 拍电影感，Veo 3 拍讲话</em>。</p>
<p>在 WorkMax 里这映射到一个工作流 — brief 写一次，按镜头路由到对应模型，平台保证角色/品牌/资产库在三个模型间保持一致。</p>
<p>→ <a href="/features/kling">Kling — 模型介绍</a> → <a href="/vs/kling-2-6-vs-sora-2">Kling 2.6 vs Sora 2 对比</a> → <a href="/tools/video-generator">打开视频生成器</a></p>',
       100, 1, 'zh',
       (SELECT id FROM (SELECT id FROM `w_blog` WHERE slug = 'kling-image-to-video' AND lang = 'en' LIMIT 1) AS en_row), 0,
       '2026-05-07 00:00:00',
       '2026-05-07 00:00:00'
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM `w_blog` WHERE slug = 'kling-image-to-video' AND lang = 'zh');

INSERT INTO `w_blog`
  (category_code, title, cover, seo_title, seo_keyword, seo_description, summary,
   slug, detail_content, sort, status, lang, main_blog_id, ai_record_id, created_at, updated_at)
SELECT 'tutorials',
       'Nano Banana Pro vs GPT Image 2：实战差异',
       '',
       'Nano Banana Pro vs GPT Image 2：实战差异',
       'Nano Banana Pro vs GPT Image 2,Nano Banana Pro review,GPT Image 2 review,best AI image model,AI image model comparison,Google Nano Banana,OpenAI GPT Image,WorkMax image model',
       '两个旗舰图像模型，各自强项不同。Nano Banana Pro 在写实和角色一致性上赢，GPT Image 2 在屏幕文字和指令服从上赢。下面教你按镜头怎么选。',
       '两个旗舰图像模型，各自强项不同。Nano Banana Pro 在写实和角色一致性上赢，GPT Image 2 在屏幕文字和指令服从上赢。下面教你按镜头怎么选。',
       'nano-banana-pro-vs-gpt-image-2',
       '<p>Nano Banana Pro 和 GPT Image 2 都是图像模型当前的第一档。在生产里它们手感很不一样，选哪个取决于活儿，不是哪个 vendor 这周发了新闻。</p>
<p>这篇把分界线划清楚，让你按镜头选而不是按订阅选。</p>
<h2>Nano Banana Pro 领先的地方</h2>
<p><strong>写实。</strong> Pro 渲染皮肤、头发、布料和零碎细节（茶杯小缺口、鞋面磨损、木桌纹理）时 AI 痕迹更少。Brief 是「看起来像一张真实照片」的时候，它是更稳的首选。</p>
<p><strong>跨渲染的角色一致性。</strong> 给一张参考帧的情况下，Pro 是当前主流图像模型里跨多次渲染保留角色面部和身体最稳的一档。品牌代言人、IP 角色、反复出现的商品 hero 主体，这是核心承载功能。</p>
<p><strong>4K 产出余量。</strong> Pro 输出 4K 不勉强。GPT Image 2 的原生上限更低；同一张终稿你得依赖一个 upscaler。</p>
<p><strong>Vertex AI 集成。</strong> 如果你的流水线在 Google Cloud，Pro 一键即接。</p>
<h2>GPT Image 2 领先的地方</h2>
<p><strong>屏幕文字。</strong> GPT Image 2 在可读、字距合理、画面内文字上仍领先 — 产品标签、广告文案、招牌、有版式层级的海报。Pro 有进步但还没追平。</p>
<p><strong>复杂 brief 的指令服从。</strong> 当 prompt 有 6+ 个约束（比如「三个人，最左拿着写字板，中间戴绿帽子，最右指向画外，1990 年代便利店，霓虹光，低机位」），GPT Image 2 跟得更多更准。</p>
<p><strong>ChatGPT 集成。</strong> 已经在 ChatGPT 里写 prompt 的话，回路更短。</p>
<p><strong>风格范围更宽。</strong> GPT Image 2 开箱在略宽一档的风格化美学上不需要 prompt 杂技就能稳定输出。</p>
<h2>它们打平的地方</h2>
<p>两个都有<strong>强色彩控制</strong>、<strong>像样的构图智能</strong>、<strong>严格的内容策略</strong>。两个都是闭源权重；要自托管哪个都不行 — 用 Stable Diffusion XL。</p>
<p>最高画质档下两个都<strong>贵</strong>。低成本用便宜的模型做发想，最终稿用旗舰。</p>
<h2>实操：每个镜头选哪个</h2>
<p>一张生产里好用的四列决策表：</p>
<table><thead><tr>
<th>镜头类型</th>
<th>选</th>
</tr></thead><tbody>
<tr>
<td>品牌 hero，含人物，写实</td>
<td>Nano Banana Pro</td>
</tr>
<tr>
<td>产品摆拍</td>
<td>Nano Banana Pro</td>
</tr>
<tr>
<td>海报 / 户外 / 画面内主标题</td>
<td>GPT Image 2</td>
</tr>
<tr>
<td>编辑插画 / 风格化</td>
<td>GPT Image 2（如有 Midjourney V7 也行）</td>
</tr>
<tr>
<td>跨集出现的角色</td>
<td>Nano Banana Pro + WorkMax 角色库</td>
</tr>
<tr>
<td>画面带文案的广告</td>
<td>GPT Image 2</td>
</tr>
<tr>
<td>视频参考帧（→ Kling / Sora 2）</td>
<td>Nano Banana Pro</td>
</tr>
</tbody></table>
<p>如果你在出反复出现的角色，叠加 WorkMax 角色库后的赢面比单纯模型对比大得多 — 同一个角色在每张 Pro 渲染<em>以及</em>每次以 Pro 帧为参考的视频渲染中都保持一致。</p>
<h2>Fan-out 战术</h2>
<p>高赌注的 hero 帧，把同一 prompt 并行送给两个模型，按维度（光线、几何、文字、角色相似度）逐项选优胜。两次渲染的成本比一次返工拍摄低得多。</p>
<p>在 WorkMax 里这是一键 — prompt 上设 fan-out，同一输入同时去 Pro 和 GPT Image 2，两个版本作为兄弟卡片落回同一画布。</p>
<h2>接下来</h2>
<p>每天 look-dev 的默认值：<strong>Nano Banana Pro</strong>。 带画面内文字的 hero：<strong>GPT Image 2</strong>。 不确定：<strong>fan-out，看输出再说</strong>。</p>
<p>→ <a href="/vs/gpt-image-2-vs-nano-banana-pro">Nano Banana Pro vs GPT Image 2 对比</a> → <a href="/features/nano-banana-pro">Nano Banana Pro — 模型介绍</a> → <a href="/tools/image-generator">打开图像生成器</a></p>',
       100, 1, 'zh',
       (SELECT id FROM (SELECT id FROM `w_blog` WHERE slug = 'nano-banana-pro-vs-gpt-image-2' AND lang = 'en' LIMIT 1) AS en_row), 0,
       '2026-05-07 00:00:00',
       '2026-05-07 00:00:00'
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM `w_blog` WHERE slug = 'nano-banana-pro-vs-gpt-image-2' AND lang = 'zh');

-- Derive cover URLs from each row's actual id (works regardless of
-- auto-increment offset across environments). The cover JPGs at these
-- paths are produced by scripts/generate_static_blog_post_covers.go
-- and uploaded to R2 via `go run ./cmd/sync_statics_to_r2 --src
-- ../web/public/images/blogcover --prefix workmax/images/blogcover`.
-- The frontend prepends NEXT_PUBLIC_ASSET_CDN to this relative path.
UPDATE `w_blog`
   SET `cover` = CONCAT('/images/blogcover/', `slug`, '_', `id`, '.jpg')
 WHERE `slug` IN (
       'sora-2-prompts-character-consistency',
       'veo-3-prompt-structure',
       'kling-image-to-video',
       'nano-banana-pro-vs-gpt-image-2'
     )
   AND `lang` = 'en'
   AND (`cover` = '' OR `cover` IS NULL);

-- Mirror the en row's cover onto its zh sibling so both locales render
-- the same hero image.
UPDATE `w_blog` zh
   JOIN `w_blog` en
     ON en.slug = zh.slug
    AND en.lang = 'en'
    AND zh.lang = 'zh'
    AND zh.main_blog_id = en.id
    SET zh.cover = en.cover
 WHERE zh.slug IN (
       'sora-2-prompts-character-consistency',
       'veo-3-prompt-structure',
       'kling-image-to-video',
       'nano-banana-pro-vs-gpt-image-2'
     )
   AND (zh.cover = '' OR zh.cover IS NULL);
