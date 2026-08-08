-- Seeds w_drama_director_preset with the 16 platform locales beyond
-- en/zh (already seeded by migration 20260560).
--
-- Translations are LLM-authored. Quality varies by locale:
--   - Confident: es, fr, de, it, pt, nl, sv, ja, ko, ru, pl, tr
--   - NEEDS HUMAN REVIEW: vi, ar, he, th — cinematography terminology
--     is technical and these translations are best-effort. Production-
--     critical use should validate with a native speaker before relying
--     on the catalogue in those markets.
--
-- Per-row WHERE NOT EXISTS keyed on (slug, lang, uid=0, project_id IS
-- NULL). Re-running is safe; correcting a translation is a follow-up
-- UPDATE rather than re-INSERT.
--
-- Composition / shot_type / camera_movement / tags stay English. They
-- are AI-prompt fuel, not user-facing copy — translating them risks
-- generation quality drift.

-- ── establishing-wide-locked ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Plano general · fijo', 'establishing-wide-locked',
  'Plano general de apertura, cámara fija. Úsalo para abrir una escena o reiniciar la ubicación.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Plan large d''établissement · fixe', 'establishing-wide-locked',
  'Plan large d''établissement, caméra fixe. À utiliser pour ouvrir une scène ou réinitialiser le lieu.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Etablierungsaufnahme · fest', 'establishing-wide-locked',
  'Weite Etablierungsaufnahme mit fester Kamera. Zum Öffnen einer Szene oder Wechseln des Schauplatzes.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Campo lungo d''apertura · fisso', 'establishing-wide-locked',
  'Campo lungo d''apertura, camera fissa. Usalo per aprire una scena o reimpostare la location.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Plano de abertura · fixo', 'establishing-wide-locked',
  'Plano geral de abertura, câmera fixa. Use para abrir uma cena ou reposicionar o local.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Vestigende totaalshot · vast', 'establishing-wide-locked',
  'Wijde vestigende shot, vaste camera. Gebruik om een scène te openen of een locatie opnieuw te introduceren.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Etableringsbild · låst', 'establishing-wide-locked',
  'Vid etableringsbild med låst kamera. Använd för att öppna en scen eller återställa platsen.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'ロング・固定ショット', 'establishing-wide-locked',
  '広角ロングショット、カメラ固定。シーンの導入やロケーションの切り替えに使用。',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '롱샷 · 고정', 'establishing-wide-locked',
  '와이드 설정 샷, 카메라 고정. 장면을 열거나 장소를 리셋할 때 사용.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Установочный план · статика', 'establishing-wide-locked',
  'Широкий установочный кадр, статичная камера. Для открытия сцены или смены локации.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Plan ogólny · statyczny', 'establishing-wide-locked',
  'Szeroki plan otwierający, kamera statyczna. Do otwarcia sceny lub resetu lokacji.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Genel plan · sabit', 'establishing-wide-locked',
  'Geniş açılış planı, kamera sabit. Sahne açmak veya mekânı sıfırlamak için kullanın.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Toàn cảnh mở · cố định', 'establishing-wide-locked',
  'Toàn cảnh thiết lập, máy quay cố định. Dùng để mở cảnh hoặc thiết lập lại bối cảnh.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'لقطة تأسيسية واسعة · ثابتة', 'establishing-wide-locked',
  'لقطة تأسيسية واسعة، كاميرا ثابتة. تُستخدم لفتح مشهد أو إعادة تأطير الموقع.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'שוט פתיחה רחב · נעול', 'establishing-wide-locked',
  'שוט פתיחה רחב, מצלמה נעולה. לפתיחת סצנה או החלפת מיקום.',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'ภาพมุมกว้างเปิดฉาก · กล้องนิ่ง', 'establishing-wide-locked',
  'ภาพมุมกว้างเปิดฉาก กล้องนิ่ง ใช้เปิดฉากหรือรีเซ็ตสถานที่',
  'establishing', 'static',
  'subject centred in the lower third, foreground leads the eye into the scene, key location landmarks visible',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'wide', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'establishing-wide-locked' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── reactive-closeup-held ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Primer plano reactivo · sostenido', 'reactive-closeup-held',
  'Primer plano del personaje que reacciona. Para resaltar un diálogo o un momento emocional silencioso.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Gros plan réactif · maintenu', 'reactive-closeup-held',
  'Gros plan du personnage qui réagit. Pour ponctuer un dialogue ou un moment émotionnel silencieux.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Reaktive Großaufnahme · gehalten', 'reactive-closeup-held',
  'Großaufnahme der reagierenden Figur. Für Dialog-Höhepunkte oder stille Emotionsmomente.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Primo piano reattivo · tenuto', 'reactive-closeup-held',
  'Primo piano del personaggio che reagisce. Per il culmine di un dialogo o un momento emotivo silenzioso.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Close-up reativo · sustentado', 'reactive-closeup-held',
  'Close-up no personagem que reage. Para o desfecho de um diálogo ou um momento emocional silencioso.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Reactieve close-up · vast', 'reactive-closeup-held',
  'Close-up op het reagerende personage. Voor de afronding van een dialoog of een stil emotioneel moment.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Reaktiv närbild · hållen', 'reactive-closeup-held',
  'Närbild på den reagerande karaktären. För dialogavslut eller tysta känslomässiga ögonblick.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'リアクション・クローズアップ', 'reactive-closeup-held',
  '反応キャラのクローズアップ。台詞の決め、または無言の感情ビートに使用。',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '리액션 클로즈업 · 유지', 'reactive-closeup-held',
  '반응하는 인물의 클로즈업. 대사 마무리 또는 무언의 감정 비트에 사용.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Реактивный крупный план · удержание', 'reactive-closeup-held',
  'Крупный план реагирующего персонажа. Для разрядки диалога или тихой эмоциональной паузы.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Zbliżenie reakcji · zatrzymane', 'reactive-closeup-held',
  'Zbliżenie na reagującą postać. Do puenty dialogu lub cichej chwili emocji.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Tepki yakın plan · sabit', 'reactive-closeup-held',
  'Tepki veren karaktere yakın plan. Diyalog vuruşu veya sessiz duygu anı için.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Cận cảnh phản ứng · giữ', 'reactive-closeup-held',
  'Cận cảnh nhân vật phản ứng. Dùng cho điểm nhấn thoại hoặc khoảnh khắc cảm xúc lặng.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'لقطة قريبة للتفاعل · ثابتة', 'reactive-closeup-held',
  'لقطة قريبة للشخصية المتفاعلة. لإبراز ذروة الحوار أو لحظة عاطفية صامتة.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'קלוז־אפ תגובה · אחיזה', 'reactive-closeup-held',
  'קלוז־אפ על הדמות המגיבה. לסיום דיאלוג או רגע רגשי שקט.',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'ภาพระยะใกล้การโต้ตอบ · กล้องค้าง', 'reactive-closeup-held',
  'ภาพระยะใกล้ของตัวละครที่โต้ตอบ ใช้สำหรับเน้นบทสนทนาหรือจังหวะอารมณ์เงียบ',
  'close-up', 'static',
  'tight frame on face, eyes on upper-third grid line, soft off-axis light, shallow depth of field',
  3, JSON_OBJECT('labels', JSON_ARRAY('close-up', 'emotion', 'static')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'reactive-closeup-held' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── push-in-reveal ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Travelling de aproximación', 'push-in-reveal',
  'Travelling lento hacia el sujeto para crear tensión o marcar una revelación.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Travelling avant révélateur', 'push-in-reveal',
  'Travelling avant lent sur le sujet pour créer de la tension ou marquer une révélation.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Heranfahrt mit Enthüllung', 'push-in-reveal',
  'Langsame Heranfahrt auf das Motiv — Spannung aufbauen oder eine Enthüllung markieren.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Carrellata in avanti rivelatrice', 'push-in-reveal',
  'Carrellata lenta in avanti sul soggetto per creare tensione o segnare una rivelazione.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Aproximação reveladora', 'push-in-reveal',
  'Aproximação lenta no sujeito para criar tensão ou marcar uma revelação.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Inrijdende onthulling', 'push-in-reveal',
  'Langzame inrijdende beweging naar het onderwerp — spanning opbouwen of een onthulling markeren.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Inåkning som avslöjar', 'push-in-reveal',
  'Långsam inåkning mot motivet — bygg spänning eller markera en avslöjande beat.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'プッシュイン・リビール', 'push-in-reveal',
  '被写体への緩やかなドリーイン。緊張感を高めたり、重要な発覚を印象づける。',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '푸시인 리빌', 'push-in-reveal',
  '피사체로 천천히 들어가는 돌리. 긴장 고조나 중요한 폭로 순간에 사용.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Наезд камеры с раскрытием', 'push-in-reveal',
  'Медленный наезд на героя — нагнетание напряжения или подчёркивание развязки.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Najazd ujawniający', 'push-in-reveal',
  'Powolny najazd na bohatera — budowanie napięcia lub zaznaczenie odkrycia.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'İçeri yaklaşma · ifşa', 'push-in-reveal',
  'Özneye doğru yavaş bir yaklaşım — gerilim yaratmak veya bir ifşa anını vurgulamak için.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Đẩy máy mở lộ', 'push-in-reveal',
  'Đẩy máy chậm vào chủ thể để tạo căng thẳng hoặc đánh dấu khoảnh khắc tiết lộ.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'تقريب الكاميرا للكشف', 'push-in-reveal',
  'تقريب بطيء على الشخصية لبناء التوتر أو إبراز لحظة كشف.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'ניגוש איטי לחשיפה', 'push-in-reveal',
  'ניגוש איטי על הדמות — בניית מתח או הדגשת רגע חשיפה.',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'ดันกล้องเปิดเผย', 'push-in-reveal',
  'ดันกล้องช้า ๆ เข้าหาตัวละคร เพื่อสร้างความตึงเครียดหรือเน้นจังหวะเปิดเผย',
  'medium', 'dolly-in',
  'subject dead-centre, background lines converge behind them, motion magnitude small so the zoom reads as deliberate',
  4, JSON_OBJECT('labels', JSON_ARRAY('push', 'tension', 'reveal')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'push-in-reveal' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── pull-back-reveal ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Travelling de retroceso revelador', 'pull-back-reveal',
  'Travelling hacia atrás que expone más del entorno — para ampliar lo que está en juego o revelar una presencia.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Travelling arrière révélateur', 'pull-back-reveal',
  'Travelling arrière dévoilant l''environnement — élargir l''enjeu ou révéler une présence.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Rückfahrt mit Enthüllung', 'pull-back-reveal',
  'Rückfahrt, die mehr von der Umgebung zeigt — um den Einsatz zu erhöhen oder eine Präsenz zu enthüllen.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Carrellata indietro rivelatrice', 'pull-back-reveal',
  'Carrellata indietro che mostra di più dell''ambiente — per ampliare la posta in gioco o rivelare una presenza.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Recuo revelador', 'pull-back-reveal',
  'Recuo da câmera expondo mais do ambiente — para ampliar a importância ou revelar uma presença.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Uitrijdende onthulling', 'pull-back-reveal',
  'Uitrijdende beweging die meer van de omgeving toont — om de inzet te verbreden of een aanwezigheid te onthullen.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Utåkning som avslöjar', 'pull-back-reveal',
  'Utåkning som visar mer av omgivningen — för att vidga insatsen eller avslöja en närvaro.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'プルバック・リビール', 'pull-back-reveal',
  'ドリーアウトで環境を露わにする。スケール拡大や存在の発覚に使用。',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '풀백 리빌', 'pull-back-reveal',
  '돌리아웃으로 환경을 더 드러냄. 스케일 확장이나 존재의 발견에 사용.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Отъезд с раскрытием', 'pull-back-reveal',
  'Отъезд камеры, открывающий больше окружения — для расширения ставок или раскрытия присутствия.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Odjazd ujawniający', 'pull-back-reveal',
  'Odjazd kamery pokazujący więcej otoczenia — do poszerzenia stawki lub ujawnienia obecności.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Geri çekilme · ifşa', 'pull-back-reveal',
  'Çevreyi açığa çıkaran geri çekiliş — bahsi büyütmek veya bir varlığı ifşa etmek için.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Lùi máy mở lộ', 'pull-back-reveal',
  'Lùi máy để lộ rộng hơn bối cảnh — mở rộng quy mô hoặc tiết lộ sự hiện diện.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'تراجع الكاميرا للكشف', 'pull-back-reveal',
  'تراجع الكاميرا لكشف المزيد من البيئة — لتوسيع الرهان أو إبراز وجود ما.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'הרחקה חושפת', 'pull-back-reveal',
  'הרחקת מצלמה החושפת עוד מהסביבה — להרחבת הסיכון או חשיפת נוכחות.',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'ถอยกล้องเผยฉาก', 'pull-back-reveal',
  'ถอยกล้องเผยให้เห็นบรรยากาศมากขึ้น เพื่อขยายเดิมพันหรือเปิดเผยการมีอยู่',
  'wide', 'dolly-out',
  'begins on subject close-up, ends with subject on rule-of-thirds intersection, wider context fills the extra frame',
  4, JSON_OBJECT('labels', JSON_ARRAY('pull', 'reveal', 'reset')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pull-back-reveal' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── shoulder-dialogue-a ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Plano sobre el hombro · A', 'shoulder-dialogue-a',
  'Sobre el hombro del personaje B, con A hablando. Lado de cobertura de diálogo estándar.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Plan par-dessus l''épaule · A', 'shoulder-dialogue-a',
  'Plan par-dessus l''épaule de B, A parle. Couverture de dialogue standard côté A.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Over-Shoulder-Dialog · A', 'shoulder-dialogue-a',
  'Über die Schulter von Figur B, Figur A spricht. Standard-Dialogabdeckung auf der A-Seite.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Sopra la spalla · A', 'shoulder-dialogue-a',
  'Sopra la spalla del personaggio B, mentre A parla. Lato standard della copertura dialogo.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Sobre o ombro · A', 'shoulder-dialogue-a',
  'Sobre o ombro de B, com A falando. Lado padrão da cobertura de diálogo.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Over-the-shoulder · A', 'shoulder-dialogue-a',
  'Over de schouder van B, met A aan het woord. Standaard dialoogdekking aan de A-kant.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Över axeln · A', 'shoulder-dialogue-a',
  'Över axeln på karaktär B, A talar. Standardtäckning av dialog på A-sidan.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'オーバーショルダー対話 · A', 'shoulder-dialogue-a',
  'Bの肩越し、Aが話す。標準的な対話カバレッジのA側。',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '오버숄더 대화 · A', 'shoulder-dialogue-a',
  'B의 어깨 너머에서 A가 말하는 컷. 표준 대화 커버리지의 A 사이드.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Через плечо · A', 'shoulder-dialogue-a',
  'Через плечо персонажа B, говорит A. Стандартная сторона покрытия диалога.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Zza ramienia · A', 'shoulder-dialogue-a',
  'Zza ramienia postaci B, mówi A. Standardowe pokrycie dialogu po stronie A.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Omuz üstü diyalog · A', 'shoulder-dialogue-a',
  'B''nin omzunun üstünden, A konuşuyor. Standart diyalog çekiminin A tarafı.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Qua vai · A', 'shoulder-dialogue-a',
  'Qua vai nhân vật B, A đang nói. Mặt chuẩn của khung quay đối thoại.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'لقطة فوق الكتف · A', 'shoulder-dialogue-a',
  'فوق كتف الشخصية B، الشخصية A تتحدث. الجانب القياسي لتغطية الحوار.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'מעל הכתף · A', 'shoulder-dialogue-a',
  'מעל הכתף של דמות B, דמות A מדברת. צד סטנדרטי לכיסוי דיאלוג.',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'มุมข้ามไหล่ · A', 'shoulder-dialogue-a',
  'ข้ามไหล่ตัวละคร B โดย A พูด ฝั่งมาตรฐานของการคัฟเวอเรจบทสนทนา',
  'over-shoulder', 'static',
  'character B''s shoulder occupies left third foreground, character A centred mid-ground, both faces catch key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-a' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── shoulder-dialogue-b ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Plano sobre el hombro · B', 'shoulder-dialogue-b',
  'Inverso de shoulder-dialogue-a. Se monta como contraplano.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Plan par-dessus l''épaule · B', 'shoulder-dialogue-b',
  'Inverse de shoulder-dialogue-a. Se monte en champ-contrechamp.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Over-Shoulder-Dialog · B', 'shoulder-dialogue-b',
  'Reverse von shoulder-dialogue-a. Schneidet zu einem Schuss-Gegenschuss-Paar.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Sopra la spalla · B', 'shoulder-dialogue-b',
  'Inverso di shoulder-dialogue-a. Si monta come campo-controcampo.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Sobre o ombro · B', 'shoulder-dialogue-b',
  'Inverso de shoulder-dialogue-a. Edita como contraplano.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Over-the-shoulder · B', 'shoulder-dialogue-b',
  'Omgekeerde van shoulder-dialogue-a. Monteert als shot-tegenshot.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Över axeln · B', 'shoulder-dialogue-b',
  'Motbild till shoulder-dialogue-a. Klipper ihop som shot/reverse shot.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'オーバーショルダー対話 · B', 'shoulder-dialogue-b',
  'shoulder-dialogue-aの切り返し。ショット・リバースショットとして編集。',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '오버숄더 대화 · B', 'shoulder-dialogue-b',
  'shoulder-dialogue-a의 역방향. 샷-리버스 샷 페어로 편집.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Через плечо · B', 'shoulder-dialogue-b',
  'Обратный к shoulder-dialogue-a. Монтируется как восьмёрка.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Zza ramienia · B', 'shoulder-dialogue-b',
  'Odwrotność shoulder-dialogue-a. Montuje się jako shot-reverse-shot.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Omuz üstü diyalog · B', 'shoulder-dialogue-b',
  'shoulder-dialogue-a''nın tersi. Karşı açı çift olarak kurgulanır.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Qua vai · B', 'shoulder-dialogue-b',
  'Đảo chiều của shoulder-dialogue-a. Dựng thành cặp đối chiều.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'لقطة فوق الكتف · B', 'shoulder-dialogue-b',
  'عكس shoulder-dialogue-a. يُركَّب كثنائي لقطة وعكسها.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'מעל הכתף · B', 'shoulder-dialogue-b',
  'ההפך של shoulder-dialogue-a. נערך כצמד שוט-רוורס-שוט.',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'มุมข้ามไหล่ · B', 'shoulder-dialogue-b',
  'กลับด้านของ shoulder-dialogue-a ตัดต่อเป็นคู่ shot-reverse-shot',
  'over-shoulder', 'static',
  'character A''s shoulder on right third foreground, character B centred mid-ground, reverse-angle of the A side',
  3, JSON_OBJECT('labels', JSON_ARRAY('dialogue', 'coverage', 'over-shoulder')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'shoulder-dialogue-b' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── handheld-pursuit ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Persecución en cámara en mano', 'handheld-pursuit',
  'Cámara en mano siguiendo al sujeto — sensación de movimiento, urgencia y subjetiva.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Poursuite caméra portée', 'handheld-pursuit',
  'Caméra portée suivant le sujet — sensation de mouvement, d''urgence, de subjectivité.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Handkamera-Verfolgung', 'handheld-pursuit',
  'Handkamera, die das Motiv verfolgt — Bewegung, Dringlichkeit, POV-Gefühl.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Inseguimento a mano', 'handheld-pursuit',
  'Camera a mano che insegue il soggetto — movimento, urgenza, sensazione di soggettiva.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Perseguição com câmera na mão', 'handheld-pursuit',
  'Câmera na mão seguindo o sujeito — sensação de movimento, urgência e ponto de vista.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Handheld achtervolging', 'handheld-pursuit',
  'Handheld camera die het onderwerp volgt — beweging, urgentie, POV-gevoel.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Handhållen förföljelse', 'handheld-pursuit',
  'Handhållen kamera som följer motivet — rörelse, brådska, POV-känsla.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'ハンドヘルド追従', 'handheld-pursuit',
  '被写体を追う手持ちカメラ。動き・緊迫感・主観感を演出。',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '핸드헬드 추격', 'handheld-pursuit',
  '피사체를 따라가는 핸드헬드 카메라 — 움직임·긴박감·POV 느낌.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Ручная камера в погоне', 'handheld-pursuit',
  'Ручная камера, следующая за героем — движение, срочность, POV-ощущение.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Pościg z ręki', 'handheld-pursuit',
  'Kamera z ręki podążająca za bohaterem — ruch, pilność, wrażenie POV.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Elden takip', 'handheld-pursuit',
  'Özneyi takip eden el kamerası — hareket, aciliyet, POV hissi.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Cầm tay đuổi theo', 'handheld-pursuit',
  'Máy cầm tay theo chân chủ thể — chuyển động, cấp bách, cảm giác POV.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'مطاردة بكاميرا محمولة باليد', 'handheld-pursuit',
  'كاميرا محمولة باليد تتبع الشخصية — إحساس بالحركة والإلحاح ووجهة النظر.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'מרדף עם מצלמת יד', 'handheld-pursuit',
  'מצלמת יד עוקבת אחר הדמות — תחושת תנועה, דחיפות וגוף ראשון.',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'กล้องมือไล่ตาม', 'handheld-pursuit',
  'กล้องมือติดตามตัวละคร ให้ความรู้สึกเคลื่อนไหว เร่งด่วน และมุมมองบุคคลที่หนึ่ง',
  'medium', 'handheld',
  'subject kept mid-frame, camera matches their pace, light head-bob adds kinetic texture without shakes that harm AI gen',
  4, JSON_OBJECT('labels', JSON_ARRAY('kinetic', 'handheld', 'follow')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'handheld-pursuit' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── crane-descent ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Descenso de grúa', 'crane-descent',
  'Grúa alta descendiendo hacia el sujeto. Abre una escena con escala y aterriza en intimidad.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Descente de grue', 'crane-descent',
  'Grue haute descendant vers le sujet. Ouvre la scène avec ampleur, atterrit sur l''intimité.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Kran-Abstieg', 'crane-descent',
  'Hoher Kran, der sich zum Motiv senkt. Öffnet eine Szene mit Größe, landet bei Intimität.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Discesa da gru', 'crane-descent',
  'Gru alta che scende verso il soggetto. Apre una scena su grande scala e atterra sull''intimità.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Descida de grua', 'crane-descent',
  'Grua alta descendo em direção ao sujeito. Abre uma cena em escala, aterrissa na intimidade.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Kraanafdaling', 'crane-descent',
  'Hoge kraan die naar het onderwerp daalt. Opent een scène groots en eindigt intiem.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Krannedstigning', 'crane-descent',
  'Hög kran som sänks mot motivet. Öppnar scenen storslaget, landar i närhet.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'クレーン降下', 'crane-descent',
  '高所からのクレーンが被写体へ降りる。スケールから親密さへ着地。',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '크레인 하강', 'crane-descent',
  '높은 크레인이 피사체로 내려옴. 스케일로 시작해 친밀함으로 착지.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Спуск крана', 'crane-descent',
  'Высокий кран, спускающийся к герою. Открывает сцену масштабом, садится в интимность.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Zejście dźwigu', 'crane-descent',
  'Wysoki dźwig schodzący do bohatera. Otwiera scenę skalą, ląduje w intymności.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Vinç inişi', 'crane-descent',
  'Yüksek vinç özneye doğru iniyor. Sahneyi ölçekle açar, yakınlıkta sonlandırır.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Cẩu hạ xuống', 'crane-descent',
  'Cẩu cao hạ dần về phía chủ thể. Mở cảnh với quy mô, kết ở sự gần gũi.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'هبوط الرافعة', 'crane-descent',
  'رافعة عالية تنزل نحو الشخصية. تفتح المشهد بحجم كبير وتهبط على الحميمية.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'ירידת מנוף', 'crane-descent',
  'מנוף גבוה יורד לעבר הדמות. פותח סצנה בגודל, נוחת באינטימיות.',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'เครนลงต่ำ', 'crane-descent',
  'เครนสูงค่อย ๆ ลดลงสู่ตัวละคร เปิดฉากด้วยสเกล แล้วลงเอยด้วยความใกล้ชิด',
  'wide', 'crane',
  'starts high three-quarters overhead, ends at shoulder level on subject; keep subject anchored at bottom-third through the move',
  5, JSON_OBJECT('labels', JSON_ARRAY('opening', 'crane', 'scale')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'crane-descent' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── insert-object-detail ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Insert · detalle de objeto', 'insert-object-detail',
  'Insert ajustado a un objeto o gesto. Para puntuación de escena o revelar un objeto clave.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Insert · détail d''objet', 'insert-object-detail',
  'Insert serré sur un accessoire ou un geste. Pour ponctuer une scène ou révéler un objet clé.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Insert · Objektdetail', 'insert-object-detail',
  'Enges Insert auf ein Requisit oder eine Geste. Zur Szenenakzentuierung oder Enthüllung eines Schlüsselobjekts.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Insert · dettaglio oggetto', 'insert-object-detail',
  'Insert stretto su un oggetto o un gesto. Per punteggiatura di scena o rivelazione di un oggetto chiave.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Insert · detalhe de objeto', 'insert-object-detail',
  'Insert fechado em um adereço ou gesto. Para pontuação de cena ou revelação de objeto-chave.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Insert · objectdetail', 'insert-object-detail',
  'Strakke insert op een prop of gebaar. Voor scène-interpunctie of onthulling van een sleutelobject.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Insert · objektdetalj', 'insert-object-detail',
  'Tät insert på en rekvisita eller gest. För scenpunktering eller avslöjande av ett nyckelobjekt.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'インサート · 小道具特写', 'insert-object-detail',
  '小道具や仕草へのタイトなインサート。シーンの句読点や鍵となる物体の提示に。',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '인서트 · 소품 디테일', 'insert-object-detail',
  '소품이나 동작에 대한 타이트한 인서트. 장면의 구두점이나 핵심 오브젝트 공개에 사용.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Инсерт · деталь объекта', 'insert-object-detail',
  'Тесный инсерт на реквизит или жест. Для пунктуации сцены или раскрытия ключевого объекта.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Insert · detal obiektu', 'insert-object-detail',
  'Ciasny insert na rekwizyt lub gest. Do interpunkcji sceny lub odsłonięcia kluczowego przedmiotu.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Insert · nesne detayı', 'insert-object-detail',
  'Bir aksesuar veya jeste sıkı insert. Sahne noktalama veya kilit nesnenin ifşası için.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Cảnh chèn · chi tiết vật thể', 'insert-object-detail',
  'Cảnh chèn chặt vào đạo cụ hoặc cử chỉ. Dùng làm dấu chấm câu hoặc tiết lộ vật thể quan trọng.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'لقطة إدراج · تفصيل غرض', 'insert-object-detail',
  'لقطة إدراج محكمة على ملحق أو إيماءة. لترقيم المشهد أو الكشف عن غرض رئيسي.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'אינסרט · פרט אובייקט', 'insert-object-detail',
  'אינסרט הדוק על אביזר או מחווה. לסימן פיסוק בסצנה או חשיפת אובייקט מפתח.',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'ภาพแทรก · รายละเอียดวัตถุ', 'insert-object-detail',
  'ภาพแทรกระยะใกล้ของอุปกรณ์ประกอบหรือท่าทาง ใช้เป็นเครื่องหมายวรรคตอนของฉากหรือเปิดเผยวัตถุสำคัญ',
  'insert', 'static',
  'object fills frame with minimal negative space, macro-adjacent lens, single-source key light to pick out texture',
  2, JSON_OBJECT('labels', JSON_ARRAY('insert', 'object', 'detail')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'insert-object-detail' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── pan-reveal-reaction ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Paneo a reacción', 'pan-reveal-reaction',
  'Paneo que termina en la reacción de un personaje. Conecta una información fuera de cuadro con la respuesta en cuadro.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Panoramique vers la réaction', 'pan-reveal-reaction',
  'Panoramique qui se termine sur la réaction d''un personnage. Pont entre une info hors-champ et la réponse à l''écran.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Schwenk auf Reaktion', 'pan-reveal-reaction',
  'Schwenk, der auf der Reaktion einer Figur endet. Brücke zwischen Off-Information und On-Reaktion.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Panoramica verso la reazione', 'pan-reveal-reaction',
  'Panoramica che termina sulla reazione di un personaggio. Collega un''informazione fuori campo alla risposta in campo.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Panorâmica até a reação', 'pan-reveal-reaction',
  'Panorâmica terminando na reação de um personagem. Liga informação fora de quadro à resposta em quadro.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Panning naar reactie', 'pan-reveal-reaction',
  'Panning die eindigt op de reactie van een personage. Brug tussen off-screen informatie en on-screen respons.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Panorering till reaktion', 'pan-reveal-reaction',
  'Panorering som slutar på en karaktärs reaktion. Bro mellan information utanför bild och respons i bild.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'リアクションへのパン', 'pan-reveal-reaction',
  'キャラクターの反応で終わるパン。画面外の情報を画面内の反応に橋渡し。',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '리액션으로 가는 패닝', 'pan-reveal-reaction',
  '인물의 반응에서 끝나는 패닝. 화면 밖 정보를 화면 안 반응으로 연결.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Панорама к реакции', 'pan-reveal-reaction',
  'Панорама, заканчивающаяся реакцией персонажа. Мост между закадровой информацией и реакцией в кадре.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Panorama do reakcji', 'pan-reveal-reaction',
  'Panorama kończąca się reakcją postaci. Most między informacją spoza kadru a reakcją w kadrze.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Tepkiye doğru pan', 'pan-reveal-reaction',
  'Bir karakterin tepkisinde biten pan çekimi. Çerçeve dışı bilgiyi çerçeve içi tepkiye bağlar.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Quét máy đến phản ứng', 'pan-reveal-reaction',
  'Quét máy kết thúc ở phản ứng của nhân vật. Cầu nối giữa thông tin ngoài khung và phản hồi trong khung.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'بانوراما إلى التفاعل', 'pan-reveal-reaction',
  'بانوراما تنتهي على ردة فعل شخصية. جسر بين معلومة خارج الإطار واستجابة داخله.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'סיבוב למענה', 'pan-reveal-reaction',
  'פאנינג שמסתיים בתגובת דמות. גשר בין מידע מחוץ למסגרת לתגובה בתוכה.',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'กล้องแพนหาปฏิกิริยา', 'pan-reveal-reaction',
  'การแพนกล้องไปจบที่ปฏิกิริยาของตัวละคร เชื่อมข้อมูลนอกเฟรมกับการตอบสนองในเฟรม',
  'medium', 'pan-right',
  'starts on the source of the news (door / phone / object), ends framed on the reacting face, motion slow enough to read both',
  4, JSON_OBJECT('labels', JSON_ARRAY('pan', 'reveal', 'reaction')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'pan-reveal-reaction' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-drama-flashback-fade ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Fundido a flashback', 'sys-drama-flashback-fade',
  'Fundido suave que evoca un recuerdo o flashback. Para enlazar pulsos del presente y del pasado.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Fondu vers flashback', 'sys-drama-flashback-fade',
  'Fondu doux évoquant un souvenir ou un flashback. Pour relier des battements du présent et du passé.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Rückblende-Einblendung', 'sys-drama-flashback-fade',
  'Sanfte Einblendung, die Erinnerung oder Rückblende evoziert. Brücke zwischen Gegenwart und Vergangenheit.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Dissolvenza in flashback', 'sys-drama-flashback-fade',
  'Dissolvenza dolce che evoca un ricordo o un flashback. Per collegare battute del presente e del passato.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Fade-in para flashback', 'sys-drama-flashback-fade',
  'Fade suave evocando memória ou flashback. Para conectar batidas do presente e do passado.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Flashback fade-in', 'sys-drama-flashback-fade',
  'Zachte fade-in die herinnering of flashback oproept. Om verleden en heden te verbinden.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Tonin till tillbakablick', 'sys-drama-flashback-fade',
  'Mjuk tonin som väcker minne eller tillbakablick. För att brygga nutid och dåtid.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', '回想フェードイン', 'sys-drama-flashback-fade',
  '記憶や回想を喚起するソフトフェードイン。現在と過去のビートを橋渡し。',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '플래시백 페이드인', 'sys-drama-flashback-fade',
  '기억이나 회상을 환기하는 부드러운 페이드인. 현재와 과거의 비트를 연결.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Затухание во флешбэк', 'sys-drama-flashback-fade',
  'Мягкое появление, вызывающее воспоминание. Мост между настоящим и прошлым.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Wprowadzenie w retrospekcję', 'sys-drama-flashback-fade',
  'Łagodne wyłonienie wywołujące wspomnienie lub retrospekcję. Łączy teraźniejszość z przeszłością.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Geri dönüşe geçiş', 'sys-drama-flashback-fade',
  'Hatıra veya flashback uyandıran yumuşak fade-in. Şimdiki ve geçmiş ritmler arasında köprü.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Mở dần vào hồi tưởng', 'sys-drama-flashback-fade',
  'Mở mờ êm dịu gợi ký ức hoặc hồi tưởng. Cầu nối giữa nhịp hiện tại và quá khứ.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'ظهور تدريجي للارتجاع', 'sys-drama-flashback-fade',
  'ظهور تدريجي ناعم يستحضر ذكرى أو ارتجاعًا زمنيًا. جسر بين نبض الحاضر والماضي.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'עמעום לפלאשבק', 'sys-drama-flashback-fade',
  'פייד-אין רך המעורר זיכרון או פלאשבק. גשר בין הווה לעבר.',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'เฟดเข้าฉากย้อนอดีต', 'sys-drama-flashback-fade',
  'การเฟดอินแบบนุ่มนวลกระตุ้นความทรงจำหรือฉากย้อนอดีต เชื่อมจังหวะปัจจุบันกับอดีต',
  'medium', 'static',
  'soft focus on subject, slight overexposure, vignette edges, dreamlike colour grade',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'flashback', 'transition')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-flashback-fade' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-drama-low-light-night ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Interior nocturno · luz baja', 'sys-drama-low-light-night',
  'Plano de interior nocturno solo con luz práctica. Para escenas tensas o íntimas tras anochecer.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Intérieur nuit · faible lumière', 'sys-drama-low-light-night',
  'Plan d''intérieur de nuit avec uniquement la lumière pratique. Pour des moments tendus ou intimes après la tombée du jour.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Schwachlicht-Nachtinnen', 'sys-drama-low-light-night',
  'Nachtinnenaufnahme nur mit Practical Lights. Für angespannte oder intime Beats nach Einbruch der Dunkelheit.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Interno notte · poca luce', 'sys-drama-low-light-night',
  'Inquadratura di interno notte con sola luce pratica. Per battute tese o intime dopo il calar del sole.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Interior noturno · pouca luz', 'sys-drama-low-light-night',
  'Plano interior noturno apenas com luz prática. Para batidas tensas ou íntimas após o anoitecer.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Nachtinterieur · weinig licht', 'sys-drama-low-light-night',
  'Nachtinterieur shot met alleen praktische lampen. Voor gespannen of intieme momenten na donker.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Nattinteriör · svagt ljus', 'sys-drama-low-light-night',
  'Nattinteriörbild med enbart praktiskt ljus. För spända eller intima beats efter mörkrets inbrott.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', '夜内景・低照度', 'sys-drama-low-light-night',
  '現場光のみで撮る夜の室内ショット。夜の緊張感や親密な場面に。',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '야간 실내 · 저조도', 'sys-drama-low-light-night',
  '프랙티컬 라이트만 쓰는 야간 실내 샷. 밤의 긴장감 또는 친밀한 비트에 사용.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Ночной интерьер · слабый свет', 'sys-drama-low-light-night',
  'Кадр ночного интерьера только с практическим светом. Для напряжённых или интимных моментов в темноте.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Wnętrze nocne · niskie światło', 'sys-drama-low-light-night',
  'Ujęcie nocnego wnętrza tylko z światłem praktycznym. Do napiętych lub intymnych momentów po zmroku.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Düşük ışıklı gece içi', 'sys-drama-low-light-night',
  'Yalnızca pratik ışıkla çekilen gece iç mekân planı. Karanlık sonrası gergin veya samimi anlar için.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Nội cảnh đêm thiếu sáng', 'sys-drama-low-light-night',
  'Cảnh nội thất ban đêm chỉ dùng đèn thực tế. Cho nhịp căng thẳng hoặc gần gũi sau khi trời tối.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'داخلي ليلي · إضاءة منخفضة', 'sys-drama-low-light-night',
  'لقطة داخلية ليلية بإضاءة عملية فقط. لنبض متوتر أو حميم بعد حلول الظلام.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'פנים לילה · תאורה נמוכה', 'sys-drama-low-light-night',
  'צילום פנים לילה עם תאורה פרקטית בלבד. לרגעים מתוחים או אינטימיים לאחר רדת החשכה.',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'ภายในกลางคืน · แสงน้อย', 'sys-drama-low-light-night',
  'ภาพในร่มยามค่ำคืนใช้เฉพาะแสงในฉาก สำหรับจังหวะตึงเครียดหรือใกล้ชิดหลังพระอาทิตย์ตก',
  'medium', 'static',
  'single warm practical light source, deep shadows on negative side of face, ambient blue fill, subject framed against dark background',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'night', 'low-light', 'mood')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-low-light-night' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-drama-confrontation-twoshot ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Plano de confrontación a dos', 'sys-drama-confrontation-twoshot',
  'Plano simétrico a dos para una confrontación. Ambos rostros legibles, tensión en el espacio entre ellos.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Plan à deux · confrontation', 'sys-drama-confrontation-twoshot',
  'Plan à deux symétrique pour une confrontation. Les deux visages lisibles, tension dans l''écart entre eux.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Konfrontations-Two-Shot', 'sys-drama-confrontation-twoshot',
  'Symmetrischer Two-Shot für eine Konfrontation. Beide Gesichter erkennbar, Spannung in der Lücke dazwischen.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Piano a due · confronto', 'sys-drama-confrontation-twoshot',
  'Piano a due simmetrico per uno scontro. Entrambi i volti leggibili, tensione nel divario tra loro.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Plano duplo · confronto', 'sys-drama-confrontation-twoshot',
  'Plano duplo simétrico para um confronto. Ambos os rostos legíveis, tensão no espaço entre eles.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Confrontatie-tweeshot', 'sys-drama-confrontation-twoshot',
  'Symmetrische tweeshot voor een confrontatie. Beide gezichten leesbaar, spanning in de ruimte ertussen.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Konfrontations-tvåbild', 'sys-drama-confrontation-twoshot',
  'Symmetrisk tvåbild för konfrontation. Båda ansiktena läsbara, spänning i utrymmet emellan.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', '対峙ツーショット', 'sys-drama-confrontation-twoshot',
  '対峙のためのシンメトリーなツーショット。両者の顔が読み取れ、間の隙間に緊張が宿る。',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '대립 투샷', 'sys-drama-confrontation-twoshot',
  '대립을 위한 대칭 투샷. 두 얼굴 모두 읽히며 그 사이 간극에 긴장감.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Двойной план противостояния', 'sys-drama-confrontation-twoshot',
  'Симметричный двойной план для противостояния. Оба лица читаемы, напряжение в зазоре между ними.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Konfrontacyjny ujęcie podwójne', 'sys-drama-confrontation-twoshot',
  'Symetryczne ujęcie dwóch postaci do konfrontacji. Obie twarze czytelne, napięcie w przestrzeni między nimi.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Yüzleşme · iki kişilik plan', 'sys-drama-confrontation-twoshot',
  'Yüzleşme için simetrik iki kişilik plan. İki yüz de okunabilir, gerilim aralarındaki boşlukta.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Cận đôi đối đầu', 'sys-drama-confrontation-twoshot',
  'Khung hình cân xứng hai người để đối đầu. Cả hai khuôn mặt đều rõ, căng thẳng nằm giữa khoảng cách hai người.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'لقطة مزدوجة للمواجهة', 'sys-drama-confrontation-twoshot',
  'لقطة مزدوجة متناظرة للمواجهة. الوجهان واضحان، والتوتر في الفراغ بينهما.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'שוט זוגי של עימות', 'sys-drama-confrontation-twoshot',
  'שוט זוגי סימטרי לעימות. שני הפנים נקראים, המתח שוכן ברווח שביניהם.',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'คู่เผชิญหน้า', 'sys-drama-confrontation-twoshot',
  'ภาพคู่สมมาตรสำหรับฉากเผชิญหน้า เห็นใบหน้าทั้งสองชัด ความตึงเครียดอยู่ในช่องว่างระหว่างกัน',
  'medium', 'static',
  'two characters facing off in profile, equal frame weight, narrow gap between them centred, hard rim light from opposing sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'confrontation', 'two-shot')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-confrontation-twoshot' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-drama-power-low-angle ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Contrapicado de poder', 'sys-drama-power-low-angle',
  'Plano medio en contrapicado que enfatiza dominio o amenaza del sujeto.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Contre-plongée de puissance', 'sys-drama-power-low-angle',
  'Plan moyen en contre-plongée soulignant la domination ou la menace du sujet.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Untersicht-Macht', 'sys-drama-power-low-angle',
  'Halbnahe Untersicht, die Dominanz oder Bedrohung des Motivs betont.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Dal basso · potere', 'sys-drama-power-low-angle',
  'Mezzo piano dal basso che sottolinea dominio o minaccia del soggetto.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Contra-plongée de poder', 'sys-drama-power-low-angle',
  'Plano médio em contra-plongée enfatizando o domínio ou a ameaça do sujeito.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Kikkerperspectief van macht', 'sys-drama-power-low-angle',
  'Medium shot vanuit kikkerperspectief, benadrukt dominantie of dreiging van het onderwerp.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Grodperspektiv av makt', 'sys-drama-power-low-angle',
  'Mediumbild i grodperspektiv som betonar dominans eller hot från motivet.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'ローアングル・威圧', 'sys-drama-power-low-angle',
  'ローアングルのミディアムショット。被写体の支配感や脅威を強調。',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '로우앵글 · 위압', 'sys-drama-power-low-angle',
  '로우앵글 미디엄 샷. 피사체의 지배감이나 위협을 강조.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Нижний ракурс · сила', 'sys-drama-power-low-angle',
  'Средний план снизу, подчёркивающий доминирование или угрозу персонажа.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Żabia perspektywa · dominacja', 'sys-drama-power-low-angle',
  'Plan średni z żabiej perspektywy, podkreśla dominację lub zagrożenie ze strony bohatera.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Alttan açı · güç', 'sys-drama-power-low-angle',
  'Özneye alttan bakan medium plan, hâkimiyet veya tehdit hissini vurgular.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Góc thấp · áp đảo', 'sys-drama-power-low-angle',
  'Cảnh trung hơi từ góc thấp, nhấn mạnh sự áp đảo hoặc đe dọa của chủ thể.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'زاوية منخفضة · قوة', 'sys-drama-power-low-angle',
  'لقطة متوسطة من زاوية منخفضة تُبرز هيمنة الشخصية أو تهديدها.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'זווית נמוכה · עוצמה', 'sys-drama-power-low-angle',
  'שוט בינוני מזווית נמוכה המדגיש שליטה או איום מצד הדמות.',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'มุมต่ำ · ข่มขวัญ', 'sys-drama-power-low-angle',
  'ภาพระยะกลางจากมุมต่ำเน้นความครอบงำหรือคุกคามของตัวละคร',
  'medium', 'static',
  'camera below subject eyeline looking up, subject fills upper two thirds, ceiling or sky negative space above, hero-style key light',
  3, JSON_OBJECT('labels', JSON_ARRAY('drama', 'power', 'low-angle')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-power-low-angle' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-drama-pov-handheld ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Cámara en mano · primera persona', 'sys-drama-pov-handheld',
  'POV en cámara en mano desde la perspectiva del protagonista. Para inmersión o secuencias de persecución.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Caméra portée · subjective', 'sys-drama-pov-handheld',
  'POV à la caméra portée depuis le point de vue du protagoniste. Pour immersion ou séquences de poursuite.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Handkamera-POV · Ich-Perspektive', 'sys-drama-pov-handheld',
  'Handheld-POV aus der Sicht der Hauptfigur. Für Immersion oder Verfolgungs-Beats.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Soggettiva a mano', 'sys-drama-pov-handheld',
  'POV a mano dal punto di vista del protagonista. Per immersione o sequenze d''inseguimento.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'POV em câmera na mão', 'sys-drama-pov-handheld',
  'POV em câmera na mão do ponto de vista do protagonista. Para imersão ou cenas de perseguição.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Handheld POV', 'sys-drama-pov-handheld',
  'Handheld POV vanuit het oogpunt van de hoofdpersoon. Voor onderdompeling of achtervolgingsmomenten.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Handhållen subjektiv', 'sys-drama-pov-handheld',
  'Handhållen POV från huvudpersonens vy. För immersion eller jaktsekvenser.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', '一人称ハンドヘルド', 'sys-drama-pov-handheld',
  '主人公視点の手持ちPOV。没入感やチェイス・ビートに使用。',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '1인칭 핸드헬드', 'sys-drama-pov-handheld',
  '주인공 시점의 핸드헬드 POV. 몰입감 또는 추격 비트에 사용.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Ручной POV от первого лица', 'sys-drama-pov-handheld',
  'Ручной POV с точки зрения протагониста. Для погружения или сцен преследования.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Subiektywka z ręki', 'sys-drama-pov-handheld',
  'Subiektywka z ręki z perspektywy bohatera. Do imersji lub scen pościgu.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Birinci şahıs el kamerası', 'sys-drama-pov-handheld',
  'Kahramanın bakış açısından el kamerasıyla POV. Daldırma veya kovalama anları için.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Cầm tay góc nhìn thứ nhất', 'sys-drama-pov-handheld',
  'POV cầm tay từ điểm nhìn của nhân vật chính. Dùng cho cảm giác hòa mình hoặc nhịp truy đuổi.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'منظور أول · كاميرا محمولة', 'sys-drama-pov-handheld',
  'وجهة نظر بالكاميرا المحمولة من منظور البطل. للانغماس أو لحظات المطاردة.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'גוף ראשון · מצלמת יד', 'sys-drama-pov-handheld',
  'POV עם מצלמת יד מנקודת המבט של הגיבור. להתעמקות או לרגעי מרדף.',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'มุมมองบุคคลที่หนึ่ง · กล้องมือ', 'sys-drama-pov-handheld',
  'POV ด้วยกล้องมือจากมุมมองตัวเอก ใช้สร้างความหลอมรวมหรือจังหวะไล่ล่า',
  'medium', 'handheld',
  'eye-level camera, slight head-bob, peripheral elements blurred, foreground hands or objects entering frame to anchor POV',
  4, JSON_OBJECT('labels', JSON_ARRAY('drama', 'pov', 'handheld', 'kinetic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-drama-pov-handheld' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-manga-panel-zoom ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Zoom-in tipo panel manga', 'sys-manga-panel-zoom',
  'Zoom-in tipo panel manga: comienza enmarcado como una viñeta, atraviesa los bordes hacia el movimiento real.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Zoom dans une case manga', 'sys-manga-panel-zoom',
  'Zoom-in façon case manga : commence cadré comme une vignette, traverse les bordures vers le mouvement réel.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Manga-Panel-Zoom', 'sys-manga-panel-zoom',
  'Manga-Panel-Zoom: beginnt wie ein Comic-Panel gerahmt, fährt über die Ränder hinaus in echte Bewegung.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Zoom-in da vignetta manga', 'sys-manga-panel-zoom',
  'Zoom-in stile vignetta manga: inizia inquadrato come un riquadro, oltrepassa i bordi nel movimento reale.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Zoom em painel de mangá', 'sys-manga-panel-zoom',
  'Zoom in em estilo painel de mangá: começa enquadrado como uma vinheta, atravessa as bordas para o movimento real.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Manga-paneel inzoom', 'sys-manga-panel-zoom',
  'Manga-paneel inzoom: begint omkaderd als stripvenster, beweegt voorbij de randen het echte beeld in.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Manga-rute inzoom', 'sys-manga-panel-zoom',
  'Manga-rute inzoom: börjar inramad som en serieruta, går förbi kanterna ut i levande bild.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', '漫画コマズームイン', 'sys-manga-panel-zoom',
  '漫画コマ風ズームイン:コマ枠で始まり、枠を抜けて実写の動きへ移行。',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '만화 컷 줌인', 'sys-manga-panel-zoom',
  '만화 컷처럼 시작해 테두리를 뚫고 실제 움직임으로 이어지는 줌인.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Зум в манга-панель', 'sys-manga-panel-zoom',
  'Зум-ин в стиле манга-панели: начало в рамке как комикс-кадр, выход за рамки в живое движение.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Najazd kadru mangi', 'sys-manga-panel-zoom',
  'Najazd w stylu kadru mangi: zaczyna oprawiony jak komiksowy kwadrat, wychodzi poza ramki w realny ruch.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Manga karesi yakınlaşma', 'sys-manga-panel-zoom',
  'Manga karesi tarzı yakınlaşma: çizgi roman karesi gibi çerçevelenir, kenarları aşıp canlı harekete geçer.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Zoom vào khung manga', 'sys-manga-panel-zoom',
  'Zoom-in kiểu khung manga: mở đầu được lồng như ô truyện, vượt khỏi viền vào chuyển động thật.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'تقريب لإطار مانغا', 'sys-manga-panel-zoom',
  'تقريب على غرار إطار المانغا: يبدأ مؤطرًا كلوحة قصص مصورة، يتجاوز الحواف إلى الحركة الحية.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'זום פנימה למסגרת מנגה', 'sys-manga-panel-zoom',
  'זום פנימה בסגנון משבצת מנגה: מתחיל ממוסגר כמו פאנל קומיקס, חורג מהגבולות לתנועה חיה.',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'ซูมเข้าแพเนลมังงะ', 'sys-manga-panel-zoom',
  'ซูมเข้าสไตล์ช่องมังงะ เริ่มในกรอบเหมือนช่องการ์ตูน แล้วข้ามขอบเข้าสู่การเคลื่อนไหวจริง',
  'medium', 'dolly-in',
  'opens with thick black panel borders framing the subject, borders peel away as camera dollies in, subject becomes full-frame and animates',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'panel', 'transition', 'opening')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-panel-zoom' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-manga-speedline-burst ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Estallido de líneas de velocidad', 'sys-manga-speedline-burst',
  'Plano de acción con líneas radiales de velocidad estallando desde el sujeto. Énfasis de impacto al estilo manga.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Éclat de lignes de vitesse', 'sys-manga-speedline-burst',
  'Plan d''action avec lignes de vitesse radiales jaillissant du sujet. Accentuation d''impact à la manière manga.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Speedline-Explosion', 'sys-manga-speedline-burst',
  'Action-Shot mit radialen Speedlines, die vom Motiv ausgehen. Manga-typische Impact-Betonung.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Esplosione di linee cinetiche', 'sys-manga-speedline-burst',
  'Inquadratura d''azione con linee cinetiche radiali che esplodono dal soggetto. Enfasi d''impatto in stile manga.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Explosão de linhas cinéticas', 'sys-manga-speedline-burst',
  'Plano de ação com linhas cinéticas radiais explodindo a partir do sujeito. Ênfase de impacto estilo mangá.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Speedline-uitbarsting', 'sys-manga-speedline-burst',
  'Actie-shot met radiale speedlines die vanuit het onderwerp uitbarsten. Manga-stijl impactaccent.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Speedline-utbrott', 'sys-manga-speedline-burst',
  'Action-bild med radiella fartlinjer som spränger ut från motivet. Manga-stil av kraftbetoning.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', '集中線バースト', 'sys-manga-speedline-burst',
  '被写体から放射状の集中線が炸裂するアクションショット。漫画的なインパクト強調。',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '스피드라인 폭발', 'sys-manga-speedline-burst',
  '피사체에서 방사형 속도선이 터져 나오는 액션 샷. 만화풍 임팩트 강조.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Взрыв скоростных линий', 'sys-manga-speedline-burst',
  'Боевой кадр с радиальными скоростными линиями, исходящими от героя. Манга-подобное усиление удара.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Wybuch linii prędkości', 'sys-manga-speedline-burst',
  'Ujęcie akcji z promieniowymi liniami prędkości buchającymi od bohatera. Manga-styl podkreślenia uderzenia.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Hız çizgileri patlaması', 'sys-manga-speedline-burst',
  'Özneden radyal hız çizgilerinin fışkırdığı aksiyon planı. Manga tarzı vuruş vurgusu.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Bùng tia tốc độ', 'sys-manga-speedline-burst',
  'Cảnh hành động với các tia tốc độ tỏa ra từ chủ thể. Nhấn mạnh tác động kiểu manga.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'انفجار خطوط السرعة', 'sys-manga-speedline-burst',
  'لقطة حركة بخطوط سرعة شعاعية تنبثق من الشخصية. تأكيد تأثير على طراز المانغا.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'התפרצות קווי מהירות', 'sys-manga-speedline-burst',
  'שוט אקשן עם קווי מהירות רדיאליים פורצים מהדמות. הדגשת אימפקט בסגנון מנגה.',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'เส้นความเร็วแตกกระจาย', 'sys-manga-speedline-burst',
  'ภาพแอ็กชันที่มีเส้นความเร็วพุ่งออกรัศมีจากตัวละคร เน้นแรงกระแทกสไตล์มังงะ',
  'close-up', 'static',
  'subject centred, radial speed lines emanating from edges toward subject, motion-blur tail behind subject, high-contrast monochrome accents',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'impact', 'speedlines')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-speedline-burst' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-manga-action-freeze ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Acción congelada', 'sys-manga-action-freeze',
  'Mantén el ápice de una acción — el momento de splash-page del manga. Breve quietud antes de continuar.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Action figée', 'sys-manga-action-freeze',
  'Maintenir au sommet d''une action — le moment splash-page du manga. Bref arrêt avant de poursuivre.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Action-Standbild', 'sys-manga-action-freeze',
  'Halt am Höhepunkt einer Aktion — der Manga-Splash-Page-Moment. Kurze Stille, bevor es weitergeht.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Azione congelata', 'sys-manga-action-freeze',
  'Tenuta al culmine di un''azione — il momento splash-page del manga. Breve fermo prima di continuare.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Ação congelada', 'sys-manga-action-freeze',
  'Sustentar no auge de uma ação — momento de splash-page do mangá. Breve pausa antes de seguir.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Bevroren actie', 'sys-manga-action-freeze',
  'Houd het hoogtepunt van een actie vast — het manga-splashpage-moment. Korte stilstand voor het verder gaat.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Frusen aktion', 'sys-manga-action-freeze',
  'Håll på en aktions kulmen — manga-splash-page-ögonblicket. Kort stillhet innan det går vidare.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'アクション・フリーズ', 'sys-manga-action-freeze',
  'アクションの頂点で停止 — 漫画の見開き的瞬間。継続前のひと呼吸。',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '액션 정지', 'sys-manga-action-freeze',
  '액션의 정점에서 멈춤 — 만화의 양면 펼침 순간. 다음 컷 전 짧은 정적.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Стоп-кадр действия', 'sys-manga-action-freeze',
  'Удержание на пике действия — момент сплэш-пейдж манги. Короткая пауза перед продолжением.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Zamrożona akcja', 'sys-manga-action-freeze',
  'Przytrzymaj szczyt akcji — moment splash-page mangi. Krótkie zatrzymanie przed dalszą sceną.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Aksiyon donması', 'sys-manga-action-freeze',
  'Aksiyonun zirvesinde duruş — manga splash-page anı. Devam etmeden önce kısa bir donma.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Đóng băng hành động', 'sys-manga-action-freeze',
  'Giữ ở đỉnh điểm hành động — khoảnh khắc trang splash của manga. Tĩnh lặng ngắn trước khi tiếp tục.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'تجميد الحركة', 'sys-manga-action-freeze',
  'ثبات عند ذروة الحركة — لحظة الصفحة الكبيرة في المانغا. توقف قصير قبل المتابعة.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'הקפאת אקשן', 'sys-manga-action-freeze',
  'החזק בשיא הפעולה — רגע ה-splash-page של המנגה. עצירה קצרה לפני שממשיכים.',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'หยุดภาพแอ็กชัน', 'sys-manga-action-freeze',
  'ค้างที่จุดสูงสุดของการเคลื่อนไหว — โมเมนต์หน้าสแปลชของมังงะ หยุดสั้น ๆ ก่อนดำเนินต่อ',
  'medium', 'static',
  'subject mid-motion frozen at peak gesture, dramatic backlight, atmospheric particles suspended, slight de-saturation to read as still frame',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'action', 'freeze', 'splash')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-action-freeze' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-manga-split-screen ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Pantalla en paneles', 'sys-manga-split-screen',
  'Pantalla dividida mostrando dos beats simultáneos — disposición multipanel estilo manga en movimiento.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Écran multipanneaux', 'sys-manga-split-screen',
  'Écran divisé montrant deux temps simultanés — mise en page multi-cases façon manga en mouvement.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Split-Panel-Screen', 'sys-manga-split-screen',
  'Split-Screen mit zwei gleichzeitigen Beats — Manga-Multi-Panel-Layout in Bewegung.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Schermo a pannelli', 'sys-manga-split-screen',
  'Schermo diviso che mostra due momenti simultanei — layout multi-vignetta stile manga in movimento.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Tela em painéis', 'sys-manga-split-screen',
  'Tela dividida mostrando duas batidas simultâneas — layout multipanel estilo mangá em movimento.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Split-paneel scherm', 'sys-manga-split-screen',
  'Splitscreen met twee gelijktijdige beats — manga-stijl multipanel-layout in beweging.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Splitruta-skärm', 'sys-manga-split-screen',
  'Delad skärm med två samtidiga beats — manga-stil flerruta-layout i rörelse.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', '分割パネル画面', 'sys-manga-split-screen',
  '二つのビートを同時表示する分割画面 — 漫画的マルチパネル構成のアニメーション版。',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '분할 패널 화면', 'sys-manga-split-screen',
  '동시에 진행되는 두 비트를 보여주는 분할 화면 — 만화풍 멀티패널을 움직임으로 옮긴 형태.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Разделённый панельный экран', 'sys-manga-split-screen',
  'Сплит-скрин с двумя одновременными битами — манга-подобный многоэкранный лэйаут в движении.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Ekran w panelach', 'sys-manga-split-screen',
  'Podzielony ekran pokazujący dwa równoległe momenty — układ wielopanelowy w stylu mangi w ruchu.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Bölmeli panel ekran', 'sys-manga-split-screen',
  'Aynı anda iki ritmi gösteren bölünmüş ekran — hareket halinde manga tarzı çoklu panel düzeni.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Màn hình chia khung', 'sys-manga-split-screen',
  'Chia màn hình hiển thị hai nhịp đồng thời — bố cục đa khung kiểu manga ở dạng chuyển động.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'شاشة بأقسام متعددة', 'sys-manga-split-screen',
  'شاشة مقسمة تعرض نبضين متزامنين — تنسيق متعدد الإطارات على طراز المانغا في حركة.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'מסך מחולק לפאנלים', 'sys-manga-split-screen',
  'מסך מפוצל המציג שני ביטים במקביל — פריסת מולטי-פאנל בסגנון מנגה בתנועה.',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'จอแบ่งช่อง', 'sys-manga-split-screen',
  'จอแบ่งแสดงสองจังหวะพร้อมกัน — เลย์เอาต์หลายช่องสไตล์มังงะในรูปแบบเคลื่อนไหว',
  'medium', 'static',
  'frame divided into two or three vertical panels with thick borders, each panel holds a different subject or angle, content advances independently',
  4, JSON_OBJECT('labels', JSON_ARRAY('manga', 'split-screen', 'multi-panel')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-split-screen' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-manga-ink-transition ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Transición a tinta', 'sys-manga-ink-transition',
  'Transición a tinta entre escenas. Como un wipe estilizado entre secuencias de manga.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Transition à l''encre', 'sys-manga-ink-transition',
  'Transition à l''encre entre scènes. Comme un volet stylisé entre séquences de manga.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Tuschwisch-Übergang', 'sys-manga-ink-transition',
  'Tuschwisch-Übergang zwischen Szenen. Stilisierter Wipe zwischen Manga-Sequenzen.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Transizione a inchiostro', 'sys-manga-ink-transition',
  'Transizione a inchiostro tra le scene. Wipe stilizzato tra sequenze di manga.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Transição em nanquim', 'sys-manga-ink-transition',
  'Transição em nanquim entre cenas. Wipe estilizado entre sequências de mangá.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Inktwash-overgang', 'sys-manga-ink-transition',
  'Inktwash-overgang tussen scènes. Gestileerde wipe tussen manga-sequenties.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Tuschövergång', 'sys-manga-ink-transition',
  'Tuschövergång mellan scener. Stiliserad wipe mellan manga-sekvenser.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', '墨絵トランジション', 'sys-manga-ink-transition',
  'シーン間の墨絵トランジション。漫画シークエンス間のスタイライズされたワイプとして。',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '수묵 전환', 'sys-manga-ink-transition',
  '장면 간 수묵 전환. 만화 시퀀스 사이의 스타일라이즈된 와이프.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Чернильный переход', 'sys-manga-ink-transition',
  'Чернильный переход между сценами. Стилизованный вайп между манга-секвенциями.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Tusznikowy przeskok', 'sys-manga-ink-transition',
  'Tusznikowy przeskok między scenami. Stylizowany wipe między sekwencjami mangi.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Mürekkep geçiş', 'sys-manga-ink-transition',
  'Sahneler arası mürekkep geçişi. Manga sekansları arasında stilize edilmiş silme.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Chuyển cảnh mực Tàu', 'sys-manga-ink-transition',
  'Chuyển cảnh mực Tàu giữa các cảnh. Như một wipe phong cách giữa các trường đoạn manga.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'انتقال بجرة حبر', 'sys-manga-ink-transition',
  'انتقال بجرة حبر بين المشاهد. مسحة منمّقة بين تتابعات المانغا.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'מעבר דיו', 'sys-manga-ink-transition',
  'מעבר דיו בין סצנות. וויפ מסוגנן בין רצפי מנגה.',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'ทรานซิชันหมึก', 'sys-manga-ink-transition',
  'การเปลี่ยนฉากด้วยรอยพู่กันหมึกระหว่างฉาก เป็นไวพ์สไตล์มังงะระหว่างซีเควนซ์',
  'wide', 'static',
  'frame fills with sweeping ink-wash brushstroke from one edge, briefly obscuring image, resolves to next scene as stroke clears',
  2, JSON_OBJECT('labels', JSON_ARRAY('manga', 'transition', 'ink', 'wipe')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-manga-ink-transition' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-ad-hero-product-spin ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Giro hero del producto', 'sys-ad-hero-product-spin',
  'Producto hero sobre fondo neutro con revelación rotacional lenta. Por defecto para anuncios de lanzamiento.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Rotation produit hero', 'sys-ad-hero-product-spin',
  'Produit hero sur fond neutre, révélation rotative lente. Par défaut pour les pubs de lancement produit.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Hero-Produkt-Drehung', 'sys-ad-hero-product-spin',
  'Hero-Produkt vor schlichtem Hintergrund mit langsamer Drehbewegung. Standard für Produkt-Launch-Spots.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Rotazione prodotto hero', 'sys-ad-hero-product-spin',
  'Prodotto hero su sfondo neutro con rivelazione rotatoria lenta. Default per spot di lancio prodotto.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Rotação hero do produto', 'sys-ad-hero-product-spin',
  'Produto hero em fundo neutro com revelação rotacional lenta. Padrão para anúncios de lançamento.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Hero-product draaiing', 'sys-ad-hero-product-spin',
  'Hero-product op een neutrale achtergrond met langzame rotatie-onthulling. Standaard voor product-launch-ads.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Hero-produkt-rotation', 'sys-ad-hero-product-spin',
  'Hero-produkt mot neutral bakgrund med långsam roterande visning. Standard för produktlanseringar.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'ヒーロー製品の回転', 'sys-ad-hero-product-spin',
  'プレーン背景上のヒーロー製品をゆっくり回転で見せる。プロダクトローンチ広告の定番。',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '히어로 제품 회전', 'sys-ad-hero-product-spin',
  '단순 배경 위 히어로 제품의 느린 회전 공개. 제품 런칭 광고의 기본 컷.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Вращение продукта-героя', 'sys-ad-hero-product-spin',
  'Продукт-герой на нейтральном фоне с медленным вращением. Стандарт для рекламы запуска.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Obrót produktu hero', 'sys-ad-hero-product-spin',
  'Produkt hero na czystym tle z powolnym obrotem. Domyślny ujęcie do reklam premierowych.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Hero ürün dönüşü', 'sys-ad-hero-product-spin',
  'Sade fonda hero ürün, yavaş dönüşle ifşa. Ürün lansmanı reklamlarının varsayılanı.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Xoay sản phẩm hero', 'sys-ad-hero-product-spin',
  'Sản phẩm hero trên nền trơn với chuyển động xoay chậm để giới thiệu. Mặc định cho quảng cáo ra mắt sản phẩm.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'دوران المنتج الرئيسي', 'sys-ad-hero-product-spin',
  'منتج رئيسي على خلفية بسيطة مع كشف دوراني بطيء. الخيار الافتراضي لإعلانات إطلاق المنتجات.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'סיבוב מוצר הירו', 'sys-ad-hero-product-spin',
  'מוצר הירו על רקע נקי בחשיפה סיבובית איטית. ברירת מחדל לפרסומות השקת מוצר.',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'หมุนสินค้าฮีโร่', 'sys-ad-hero-product-spin',
  'สินค้าฮีโร่บนพื้นหลังเรียบ หมุนเปิดตัวอย่างช้า ๆ เป็นค่ามาตรฐานสำหรับโฆษณาเปิดตัวสินค้า',
  'medium', 'tracking',
  'product centred on seamless backdrop, camera arcs 30-60 degrees around it, soft three-point key/fill/rim, no distracting props',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'product', 'hero', 'rotation')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-hero-product-spin' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-ad-ugc-selfie ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Selfie estilo UGC', 'sys-ad-ugc-selfie',
  'Aspecto UGC auténtico — encuadre tipo selfie en mano, escenario informal, talento hablando a cámara.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Selfie UGC', 'sys-ad-ugc-selfie',
  'Look UGC authentique — cadrage selfie à la main, décor décontracté, talent qui s''adresse à la caméra.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'UGC-Selfie', 'sys-ad-ugc-selfie',
  'Authentischer UGC-Look — Handheld-Selfie-Framing, lockeres Setting, Talent spricht in die Kamera.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Selfie stile UGC', 'sys-ad-ugc-selfie',
  'Look UGC autentico — inquadratura selfie a mano, ambiente informale, talent che parla alla camera.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Selfie estilo UGC', 'sys-ad-ugc-selfie',
  'Visual UGC autêntico — enquadramento selfie na mão, cenário casual, apresentador falando para a câmera.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'UGC-selfie', 'sys-ad-ugc-selfie',
  'Authentieke UGC-look — handheld selfie-kader, casual setting, talent praat in de camera.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'UGC-selfie', 'sys-ad-ugc-selfie',
  'Autentisk UGC-känsla — handhållen selfie-bildutsnitt, vardaglig miljö, talangen pratar in i kameran.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'UGC自撮り', 'sys-ad-ugc-selfie',
  'リアルなUGC感 — 手持ち自撮りフレーミング、生活感のある背景、カメラに向かって話す。',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', 'UGC 셀피', 'sys-ad-ugc-selfie',
  '리얼 UGC 룩 — 핸드헬드 셀피 프레임, 캐주얼한 배경, 인물이 카메라를 향해 말함.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'UGC-селфи', 'sys-ad-ugc-selfie',
  'Аутентичный UGC-вид — селфи с рук, бытовой фон, спикер говорит в камеру.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'UGC selfie', 'sys-ad-ugc-selfie',
  'Autentyczny look UGC — selfie z ręki, swobodne otoczenie, prezenter mówi do kamery.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'UGC selfie', 'sys-ad-ugc-selfie',
  'Otantik UGC görünümü — el ile selfie kadrajı, gündelik ortam, sunucu kameraya konuşuyor.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Selfie kiểu UGC', 'sys-ad-ugc-selfie',
  'Phong cách UGC chân thực — khung selfie cầm tay, bối cảnh đời thường, người nói trực tiếp với máy quay.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'سيلفي بأسلوب UGC', 'sys-ad-ugc-selfie',
  'مظهر UGC أصيل — تأطير سيلفي محمول باليد، بيئة عادية، الشخصية تتحدث للكاميرا.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'סלפי בסגנון UGC', 'sys-ad-ugc-selfie',
  'מראה UGC אותנטי — מסגור סלפי מהיד, סביבה יומיומית, היוצר מדבר אל המצלמה.',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'เซลฟี่สไตล์ UGC', 'sys-ad-ugc-selfie',
  'ลุค UGC จริง ๆ — กรอบภาพเซลฟี่ถือมือ ฉากดูเป็นกันเอง ผู้พูดหันพูดกับกล้องโดยตรง',
  'close-up', 'handheld',
  'arm-length selfie framing, talent eyeline into lens, real-world lived-in background, available natural light, slight off-centre composition',
  5, JSON_OBJECT('labels', JSON_ARRAY('ad', 'ugc', 'selfie', 'authentic')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-ugc-selfie' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-ad-pain-point-cut ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Corte de punto de dolor', 'sys-ad-pain-point-cut',
  'Corte en dos tiempos que yuxtapone el dolor (antes) y el alivio (después). Estructura común de hook publicitario.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Cut « pain point »', 'sys-ad-pain-point-cut',
  'Cut en deux temps opposant la douleur (avant) au soulagement (après). Structure de hook publicitaire classique.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Pain-Point-Cut', 'sys-ad-pain-point-cut',
  'Zwei-Beat-Cut, der Schmerz (vorher) und Erleichterung (nachher) gegenüberstellt. Klassische Werbe-Hook-Struktur.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Cut del pain-point', 'sys-ad-pain-point-cut',
  'Cut a due battute che contrappone il problema (prima) e il sollievo (dopo). Struttura di hook pubblicitario comune.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Corte de pain point', 'sys-ad-pain-point-cut',
  'Corte de duas batidas opondo dor (antes) e alívio (depois). Estrutura comum de hook publicitário.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Pain-point cut', 'sys-ad-pain-point-cut',
  'Cut in twee beats die pijn (voor) tegenover verlichting (na) zet. Klassieke advertentie-hook structuur.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Smärtpunkts-cut', 'sys-ad-pain-point-cut',
  'Två-beat-cut som ställer smärta (före) mot lättnad (efter). Vanlig reklam-hook-struktur.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'ペインポイント・カット', 'sys-ad-pain-point-cut',
  '痛み(Before)と解消(After)を対比する2ビート・カット。広告フックの定番構造。',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '페인 포인트 컷', 'sys-ad-pain-point-cut',
  '고통(Before)과 해소(After)를 대비하는 2비트 컷. 흔한 광고 훅 구조.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Кат через боль', 'sys-ad-pain-point-cut',
  'Двух-битный кат, противопоставляющий боль (до) и облегчение (после). Стандартная структура рекламного хука.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Cut pain-point', 'sys-ad-pain-point-cut',
  'Dwutaktowy cut zestawiający ból (przed) i ulgę (po). Częsta struktura haka reklamowego.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Pain-point kesimi', 'sys-ad-pain-point-cut',
  'Acıyı (öncesi) ve rahatlamayı (sonrası) karşılaştıran iki vuruşlu kesim. Yaygın reklam çengeli yapısı.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Cắt điểm đau', 'sys-ad-pain-point-cut',
  'Cắt hai nhịp đối lập nỗi đau (trước) và sự giải tỏa (sau). Cấu trúc hook quảng cáo phổ biến.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'قطع نقطة الألم', 'sys-ad-pain-point-cut',
  'قطع بنبضين يقابل بين المشكلة (قبل) والراحة (بعد). بنية شائعة لخطاف الإعلان.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'קאט של נקודת כאב', 'sys-ad-pain-point-cut',
  'קאט בשני ביטים המעמיד כאב (לפני) מול הקלה (אחרי). מבנה הוק פרסומי נפוץ.',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'ตัดต่อชี้จุดเจ็บ', 'sys-ad-pain-point-cut',
  'การตัดสองจังหวะเปรียบเทียบความเจ็บปวด (ก่อน) กับการบรรเทา (หลัง) เป็นโครงสร้างฮุกโฆษณาที่พบบ่อย',
  'medium', 'static',
  'first half: subject struggling with the problem, desaturated grade, cluttered frame; cut to clean composition, brighter grade, problem solved',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'pain-point', 'before-after', 'hook')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-pain-point-cut' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-ad-cta-end-card ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Card final con CTA', 'sys-ad-cta-end-card',
  'Composición de card final para el CTA / marca. Manténlo el tiempo suficiente para leerse.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Carton de fin avec CTA', 'sys-ad-cta-end-card',
  'Composition de carton de fin pour le CTA / la marque. Maintenir assez longtemps pour être lu.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'CTA-Endcard', 'sys-ad-cta-end-card',
  'Endcard-Komposition für den CTA / das Markenlogo. Lange genug halten, um lesbar zu sein.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Endcard con CTA', 'sys-ad-cta-end-card',
  'Composizione di endcard per la CTA / il marchio. Tienila abbastanza a lungo da essere letta.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Endcard com CTA', 'sys-ad-cta-end-card',
  'Composição de endcard para o CTA / marca. Mantenha tempo suficiente para ser lido.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'CTA-endcard', 'sys-ad-cta-end-card',
  'Endcard-compositie voor de CTA / het merk. Houd lang genoeg vast om gelezen te worden.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'CTA-slutbild', 'sys-ad-cta-end-card',
  'Slutbildskomposition för CTA / varumärket. Håll tillräckligt länge för att hinna läsas.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'CTA エンドカード', 'sys-ad-cta-end-card',
  'CTAやブランドマークのためのエンドカード構図。読み取れる長さで保持。',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', 'CTA 엔드 카드', 'sys-ad-cta-end-card',
  'CTA / 브랜드 마크용 엔드 카드 구도. 읽힐 수 있는 시간만큼 유지.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Финальная карточка CTA', 'sys-ad-cta-end-card',
  'Финальная карточка для CTA / бренда. Удерживать достаточно долго для прочтения.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Endcard z CTA', 'sys-ad-cta-end-card',
  'Kompozycja endcardu dla CTA / marki. Utrzymaj wystarczająco długo, by widz zdążył przeczytać.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'CTA bitiş kartı', 'sys-ad-cta-end-card',
  'CTA / marka için bitiş kartı kompozisyonu. Okunabilecek kadar uzun tutun.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Khung CTA cuối', 'sys-ad-cta-end-card',
  'Bố cục khung kết thúc cho CTA / thương hiệu. Giữ đủ lâu để khán giả đọc.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'بطاقة نهاية CTA', 'sys-ad-cta-end-card',
  'تكوين بطاقة النهاية لزر CTA أو علامة المنتج. ابقها فترة تكفي للقراءة.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'כרטיס סיום CTA', 'sys-ad-cta-end-card',
  'קומפוזיציית כרטיס סיום לקריאה לפעולה / לוגו המותג. החזק זמן מספיק כדי לקרוא.',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'การ์ดปิดท้าย CTA', 'sys-ad-cta-end-card',
  'องค์ประกอบการ์ดปิดท้ายสำหรับ CTA / โลโก้แบรนด์ ค้างนานพอให้อ่านได้',
  'wide', 'static',
  'brand logo on rule-of-thirds intersection, CTA text on opposing third, clean negative space, brand-palette background, no motion',
  3, JSON_OBJECT('labels', JSON_ARRAY('ad', 'cta', 'end-card', 'brand')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-cta-end-card' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-ad-before-after-split ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Split antes/después', 'sys-ad-before-after-split',
  'Split de antes/después en una sola toma. Sin corte — comparación a pantalla dividida en una única captura.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Split avant/après', 'sys-ad-before-after-split',
  'Split avant/après en un seul plan. Pas de cut — comparaison split-screen dans une seule prise.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Vorher/Nachher-Split', 'sys-ad-before-after-split',
  'Single-Frame-Vorher/Nachher-Split. Ohne Schnitt — Splitscreen-Vergleich in einer Einstellung.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Split prima/dopo', 'sys-ad-before-after-split',
  'Split prima/dopo in una sola inquadratura. Senza tagli — confronto a schermo diviso in un''unica ripresa.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Split antes/depois', 'sys-ad-before-after-split',
  'Split antes/depois em um único frame. Sem corte — comparação em tela dividida em uma só tomada.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Voor/na split', 'sys-ad-before-after-split',
  'Voor/na splitscreen in één frame. Geen cut — splitscreen-vergelijking in één take.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Före/efter-split', 'sys-ad-before-after-split',
  'Före/efter splitscreen i en bild. Inget klipp — jämförelse i delad bild på en tagning.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'ビフォー/アフター スプリット', 'sys-ad-before-after-split',
  'ワンショット内のビフォー/アフター・スプリット画面。カット無しで分割比較。',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '비포/애프터 스플릿', 'sys-ad-before-after-split',
  '한 컷 안의 전후 분할 화면. 컷 없이 한 테이크에서 분할 비교.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'До/после в сплит-скрине', 'sys-ad-before-after-split',
  'До/после в одном кадре. Без склейки — сравнение через сплит-скрин в одном дубле.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Split przed/po', 'sys-ad-before-after-split',
  'Split przed/po w jednym ujęciu. Bez cięcia — porównanie podzielonego ekranu w jednym duble.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Önce/sonra bölünmesi', 'sys-ad-before-after-split',
  'Tek karede önce/sonra bölünmüş ekran. Kesmeden — tek çekimde split-screen karşılaştırması.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Tách trước/sau', 'sys-ad-before-after-split',
  'Chia màn hình trước/sau trong một khung hình. Không cắt — so sánh split-screen trong một lượt quay.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'تقسيم قبل/بعد', 'sys-ad-before-after-split',
  'تقسيم قبل/بعد في إطار واحد. بلا قطع — مقارنة بشاشة مقسمة في لقطة واحدة.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'פיצול לפני/אחרי', 'sys-ad-before-after-split',
  'פיצול מסך לפני/אחרי בתוך פריים אחד. ללא חיתוך — השוואה במסך מפוצל בטייק אחד.',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'แบ่งก่อน/หลัง', 'sys-ad-before-after-split',
  'แบ่งจอก่อน/หลังในเฟรมเดียว ไม่ตัด — เปรียบเทียบสปลิตสกรีนในการถ่ายเดียว',
  'medium', 'static',
  'frame split vertically down the centre, "before" state on left half (muted), "after" state on right half (vivid), matched framing on both sides',
  4, JSON_OBJECT('labels', JSON_ARRAY('ad', 'before-after', 'split-screen', 'comparison')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ad-before-after-split' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-ecom-product-360 ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Producto 360°', 'sys-ecom-product-360',
  'Rotación completa de 360° en plato giratorio. Vídeo estándar de listado e-commerce.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Produit 360°', 'sys-ecom-product-360',
  'Rotation complète à 360° sur tour à plateau. Vidéo standard de fiche e-commerce.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Produkt 360°', 'sys-ecom-product-360',
  'Volle 360°-Drehung auf Drehteller. Standard-E-Commerce-Listing-Video.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Prodotto 360°', 'sys-ecom-product-360',
  'Rotazione completa a 360° su piano girevole. Video standard di scheda e-commerce.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Produto 360°', 'sys-ecom-product-360',
  'Rotação completa de 360° em base giratória. Vídeo padrão de listagem e-commerce.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Product 360°', 'sys-ecom-product-360',
  'Volledige 360°-rotatie op draaitafel. Standaard productpagina-video voor e-commerce.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Produkt 360°', 'sys-ecom-product-360',
  'Fullständig 360°-rotation på vridbord. Standardvideo för e-handelslistningar.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', '製品360°', 'sys-ecom-product-360',
  'ターンテーブルでの360°フル回転。Eコマース商品ページの定番動画。',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '제품 360°', 'sys-ecom-product-360',
  '턴테이블 위 360° 완전 회전. 이커머스 상세 페이지의 기본 영상.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Продукт 360°', 'sys-ecom-product-360',
  'Полный поворот на 360° на поворотном столе. Стандартное видео карточки товара.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Produkt 360°', 'sys-ecom-product-360',
  'Pełen obrót 360° na obrotnicy. Standardowe wideo karty produktu w e-commerce.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Ürün 360°', 'sys-ecom-product-360',
  'Döner tabla üzerinde tam 360° dönüş. E-ticaret ürün sayfası için standart video.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Sản phẩm 360°', 'sys-ecom-product-360',
  'Xoay tròn 360° trên bàn xoay. Video tiêu chuẩn cho trang chi tiết sản phẩm thương mại điện tử.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'منتج 360°', 'sys-ecom-product-360',
  'دوران كامل 360° على طاولة دوارة. فيديو قياسي لصفحة منتج في التجارة الإلكترونية.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'מוצר 360°', 'sys-ecom-product-360',
  'סיבוב מלא של 360° על שולחן סיבובי. וידאו סטנדרטי לדף מוצר במסחר אלקטרוני.',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'สินค้า 360°', 'sys-ecom-product-360',
  'หมุน 360° เต็มวงบนแท่นหมุน วิดีโอมาตรฐานสำหรับหน้ารายการสินค้าอีคอมเมิร์ซ',
  'medium', 'tracking',
  'product centred on seamless turntable, camera locked, full 360 rotation in even pace, neutral white or branded backdrop',
  6, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'product', '360', 'turntable')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-product-360' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-ecom-detail-macro ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Macro de detalle', 'sys-ecom-detail-macro',
  'Insert macro que destaca material, costura o textura. Para transmitir artesanía y calidad.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Macro de détail', 'sys-ecom-detail-macro',
  'Insert macro mettant en valeur matière, couture ou texture. Pour transmettre savoir-faire et qualité.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Makro-Detail', 'sys-ecom-detail-macro',
  'Makro-Insert, das Material, Naht oder Textur betont. Um Handwerk und Qualität zu vermitteln.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Macro di dettaglio', 'sys-ecom-detail-macro',
  'Insert macro che evidenzia materiale, cuciture o texture. Per trasmettere artigianalità e qualità.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Macro de detalhe', 'sys-ecom-detail-macro',
  'Insert macro destacando material, costura ou textura. Para transmitir cuidado e qualidade.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Detailmacro', 'sys-ecom-detail-macro',
  'Macro-insert die materiaal, stiksel of textuur uitlicht. Om vakmanschap en kwaliteit over te brengen.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Detaljmakro', 'sys-ecom-detail-macro',
  'Makro-insert som lyfter fram material, sömmar eller textur. För att förmedla hantverk och kvalitet.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'ディテール・マクロ', 'sys-ecom-detail-macro',
  '素材・縫い目・質感を強調するマクロ・インサート。職人技や品質を伝える。',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '디테일 매크로', 'sys-ecom-detail-macro',
  '소재·박음질·텍스처를 부각하는 매크로 인서트. 장인정신과 품질을 전달.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Макро деталей', 'sys-ecom-detail-macro',
  'Макро-инсерт, выделяющий материал, шов или текстуру. Для передачи мастерства и качества.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Makro detalu', 'sys-ecom-detail-macro',
  'Makro-insert eksponujący materiał, szew lub fakturę. Do pokazania rzemiosła i jakości.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Detay makrosu', 'sys-ecom-detail-macro',
  'Malzeme, dikiş veya dokuyu öne çıkaran makro insert. Zanaat ve kaliteyi anlatmak için.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Macro chi tiết', 'sys-ecom-detail-macro',
  'Cảnh chèn macro làm nổi bật chất liệu, đường may hoặc kết cấu. Truyền tải sự tinh tế và chất lượng.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'ماكرو تفاصيل', 'sys-ecom-detail-macro',
  'لقطة إدراج ماكرو تُبرز الخامة أو الخياطة أو الملمس. لإيصال الحرفية والجودة.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'מאקרו פרט', 'sys-ecom-detail-macro',
  'אינסרט מאקרו המדגיש חומר, תפר או טקסטורה. להעברת אומנות ואיכות.',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'มาโครรายละเอียด', 'sys-ecom-detail-macro',
  'ภาพแทรกมาโครเน้นเนื้อวัสดุ ตะเข็บ หรือพื้นผิว เพื่อสื่อความประณีตและคุณภาพ',
  'insert', 'dolly-in',
  'macro lens framing on a single material detail, very shallow depth of field, single grazing key light to bring out texture, slow push-in',
  3, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'macro', 'detail', 'texture')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-detail-macro' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-ecom-usage-scene ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Uso lifestyle', 'sys-ecom-usage-scene',
  'Plano lifestyle mostrando el producto en uso real. Conecta funcionalidad con contexto.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Usage lifestyle', 'sys-ecom-usage-scene',
  'Plan lifestyle montrant le produit en usage réel. Relie la fonctionnalité au contexte.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Lifestyle-Nutzung', 'sys-ecom-usage-scene',
  'Lifestyle-Aufnahme, die das Produkt im echten Einsatz zeigt. Verbindet Funktion mit Kontext.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Uso lifestyle', 'sys-ecom-usage-scene',
  'Inquadratura lifestyle che mostra il prodotto in uso reale. Collega la funzionalità al contesto.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Uso lifestyle', 'sys-ecom-usage-scene',
  'Plano lifestyle mostrando o produto em uso real. Conecta funcionalidade ao contexto.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Lifestyle-gebruik', 'sys-ecom-usage-scene',
  'Lifestyle-shot dat het product in echt gebruik toont. Verbindt functie met context.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Lifestyle-användning', 'sys-ecom-usage-scene',
  'Lifestyle-bild som visar produkten i verklig användning. Kopplar funktion till sammanhang.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', 'ライフスタイル使用シーン', 'sys-ecom-usage-scene',
  '実生活で製品を使う様子を映すライフスタイル・ショット。機能と使用シーンを結びつける。',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '라이프스타일 사용 장면', 'sys-ecom-usage-scene',
  '제품이 실제로 사용되는 모습을 담는 라이프스타일 샷. 기능과 사용 맥락을 연결.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Лайфстайл-использование', 'sys-ecom-usage-scene',
  'Лайфстайл-кадр, показывающий продукт в реальном использовании. Связывает функцию и контекст.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Użycie lifestyle', 'sys-ecom-usage-scene',
  'Ujęcie lifestylowe pokazujące produkt w prawdziwym użyciu. Łączy funkcję z kontekstem.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Lifestyle kullanım', 'sys-ecom-usage-scene',
  'Ürünü gerçek kullanımda gösteren lifestyle planı. Özelliği bağlama bağlar.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Lifestyle sử dụng thật', 'sys-ecom-usage-scene',
  'Cảnh lifestyle thể hiện sản phẩm được dùng trong đời thật. Kết nối tính năng với bối cảnh.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'استخدام بأسلوب حياة', 'sys-ecom-usage-scene',
  'لقطة بأسلوب حياة تُظهر المنتج في استخدام فعلي. تربط الميزة بالسياق.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'שימוש לייפסטייל', 'sys-ecom-usage-scene',
  'צילום לייפסטייל המראה את המוצר בשימוש אמיתי. מחבר תכונה להקשר.',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'ภาพไลฟ์สไตล์การใช้งาน', 'sys-ecom-usage-scene',
  'ภาพไลฟ์สไตล์แสดงสินค้าในการใช้งานจริง เชื่อมฟีเจอร์กับบริบทการใช้งาน',
  'medium', 'static',
  'product in active use by talent in a believable real-world setting, talent partially in frame, environmental cues reinforce use case',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'lifestyle', 'usage', 'context')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-usage-scene' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-ecom-feature-callout ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Callout de característica', 'sys-ecom-feature-callout',
  'Callout de característica — producto en pantalla con etiquetas de texto que se animan para destacar puntos de venta.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Callout de fonctionnalité', 'sys-ecom-feature-callout',
  'Callout de fonctionnalité — produit à l''écran avec des étiquettes texte animées qui mettent en avant les arguments de vente.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Feature-Callout', 'sys-ecom-feature-callout',
  'Feature-Callout — Produkt im Bild, Textlabels animieren ein, um Verkaufsargumente hervorzuheben.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Callout di funzionalità', 'sys-ecom-feature-callout',
  'Callout di funzionalità — prodotto in schermo con etichette di testo animate che evidenziano i punti di vendita.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Callout de feature', 'sys-ecom-feature-callout',
  'Callout de feature — produto na tela com rótulos de texto animados destacando pontos de venda.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Feature-callout', 'sys-ecom-feature-callout',
  'Feature-callout — product in beeld met tekstlabels die inanimeren om verkooppunten te benoemen.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Funktions-callout', 'sys-ecom-feature-callout',
  'Funktions-callout — produkten på skärmen med textetiketter som animeras in och pekar ut säljargument.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', '機能コールアウト', 'sys-ecom-feature-callout',
  '機能コールアウト — 製品が画面に表示され、セールスポイントを示すテキストラベルがアニメーションで現れる。',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '피처 콜아웃', 'sys-ecom-feature-callout',
  '피처 콜아웃 — 화면 속 제품에 텍스트 라벨이 애니메이션으로 등장해 셀링 포인트를 짚어줌.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Подсветка функции', 'sys-ecom-feature-callout',
  'Подсветка функции — продукт в кадре с анимированными текстовыми метками, выделяющими преимущества.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Callout funkcji', 'sys-ecom-feature-callout',
  'Callout funkcji — produkt na ekranie z animowanymi etykietami tekstu wskazującymi atuty sprzedażowe.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Özellik balonu', 'sys-ecom-feature-callout',
  'Özellik balonu — ekrandaki ürün üzerine satış noktalarını işaret eden animasyonlu metin etiketleri açılır.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Chú thích tính năng', 'sys-ecom-feature-callout',
  'Chú thích tính năng — sản phẩm trên màn hình với nhãn chữ xuất hiện động để nhấn các điểm bán hàng.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'شرح تعريفي للمزايا', 'sys-ecom-feature-callout',
  'شرح تعريفي للمزايا — المنتج على الشاشة مع عناوين نصية تظهر بحركة لإبراز نقاط البيع.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'הערת תכונה', 'sys-ecom-feature-callout',
  'הערת תכונה — המוצר על המסך עם תוויות טקסט שמופיעות באנימציה ומסמנות נקודות מכירה.',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'คำชี้จุดเด่น', 'sys-ecom-feature-callout',
  'คำชี้จุดเด่น — สินค้าในจอพร้อมป้ายข้อความเลื่อนเข้ามาชี้จุดขายที่สำคัญ',
  'medium', 'static',
  'product framed with generous negative space on one side for callout text, callout text fades in pinned to the relevant product part',
  4, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'feature', 'callout', 'caption')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-feature-callout' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

-- ── sys-ecom-unboxing ──
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'es', 'Unboxing en primera persona', 'sys-ecom-unboxing',
  'Ángulo de unboxing en primera persona — manos del talento abriendo el empaque, con el paquete centrado.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'es' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'fr', 'Unboxing à la première personne', 'sys-ecom-unboxing',
  'Plan d''unboxing à la première personne — mains du talent ouvrant l''emballage, paquet centré dans le cadre.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'fr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'de', 'Unboxing-POV', 'sys-ecom-unboxing',
  'Erste-Person-Unboxing-Winkel — Hände des Talents öffnen die Verpackung, Paket mittig im Bild.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'de' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'it', 'Unboxing in soggettiva', 'sys-ecom-unboxing',
  'Angolazione unboxing in prima persona — mani del talent che aprono la confezione, pacco centrato nel frame.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'it' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pt', 'Unboxing em primeira pessoa', 'sys-ecom-unboxing',
  'Ângulo de unboxing em primeira pessoa — mãos do apresentador abrindo a embalagem, pacote centralizado.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'pt' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'nl', 'Unboxing-POV', 'sys-ecom-unboxing',
  'Eerste-persoons unboxing-hoek — handen van de talent openen de verpakking, pakket centraal in beeld.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'nl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'sv', 'Unboxing-POV', 'sys-ecom-unboxing',
  'Förstapersons unboxing-vinkel — talangens händer öppnar förpackningen, paketet centrerat i bilden.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'sv' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ja', '一人称アンボクシング', 'sys-ecom-unboxing',
  '一人称視点のアンボクシング — タレントの両手がパッケージを開封、フレーム中央に商品箱。',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'ja' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ko', '1인칭 언박싱', 'sys-ecom-unboxing',
  '1인칭 시점 언박싱 — 인물의 두 손이 패키지를 열고, 박스가 화면 중앙에 위치.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'ko' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ru', 'Распаковка от первого лица', 'sys-ecom-unboxing',
  'Угол распаковки от первого лица — руки модели открывают упаковку, коробка по центру кадра.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'ru' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'pl', 'Unboxing pierwszoosobowy', 'sys-ecom-unboxing',
  'Pierwszoosobowy unboxing — dłonie prezentera otwierają opakowanie, paczka w centrum kadru.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'pl' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'tr', 'Birinci şahıs kutu açılışı', 'sys-ecom-unboxing',
  'Birinci şahıs kutu açılış açısı — sunucunun elleri ambalajı açar, kutu kadrajın merkezinde.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'tr' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'vi', 'Mở hộp góc nhìn thứ nhất', 'sys-ecom-unboxing',
  'Góc mở hộp góc nhìn thứ nhất — tay của người dẫn mở bao bì, hộp ở giữa khung hình.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'vi' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'ar', 'فتح العلبة من منظور أول', 'sys-ecom-unboxing',
  'زاوية فتح علبة من منظور أول — يدا الشخصية تفتحان العبوة، العلبة في وسط الإطار.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'ar' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'he', 'פתיחת אריזה בגוף ראשון', 'sys-ecom-unboxing',
  'זווית פתיחת אריזה בגוף ראשון — ידי היוצר פותחות את האריזה, החבילה במרכז הפריים.',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'he' AND `uid` = 0 AND `project_id` IS NULL
);
INSERT INTO `w_drama_director_preset`
  (`uid`, `project_id`, `lang`, `name`, `slug`, `description`, `shot_type`, `camera_movement`, `composition`, `duration_hint`, `tags_json`, `status`)
SELECT 0, NULL, 'th', 'แกะกล่องมุมมองบุคคลที่หนึ่ง', 'sys-ecom-unboxing',
  'มุมแกะกล่องจากมุมมองบุคคลที่หนึ่ง — มือของผู้พรีเซนต์เปิดบรรจุภัณฑ์ กล่องอยู่กลางเฟรม',
  'close-up', 'static',
  'top-down or shoulder-cam over hands, package centred, hands enter from bottom of frame to open it, even diffuse light, no other distractions',
  5, JSON_OBJECT('labels', JSON_ARRAY('ecom', 'unboxing', 'pov', 'first-person')), 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_director_preset` WHERE `slug` = 'sys-ecom-unboxing' AND `lang` = 'th' AND `uid` = 0 AND `project_id` IS NULL
);

