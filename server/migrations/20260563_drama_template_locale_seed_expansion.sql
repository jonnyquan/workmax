-- Seeds w_drama_template with the 16 platform locales beyond en/zh
-- (already seeded by migration 20260561).
--
-- Translations are LLM-authored. Same quality caveat as 20260562:
--   - Confident: es, fr, de, it, pt, nl, sv, ja, ko, ru, pl, tr
--   - NEEDS HUMAN REVIEW: vi, ar, he, th — short-drama genre terms
--     ("CEO drama", "time-travel comeback", "underdog rising") are
--     culturally specific and translations are best-effort.
--
-- Per-row WHERE NOT EXISTS keyed on (slug, lang, is_system=1).
-- Structural fields (genre, episode counts, aspect ratio, style preset,
-- target platform, emotional template) stay identical across locales —
-- only the user-facing text columns translate.

-- ── sweet-romance ──
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'es', 'Romance dulce',
  'Romance cotidiano ligero y dulce — cada episodio tiene un pequeño conflicto que se resuelve en un momento dulce. Ideal para cuentas matriz de publicación diaria.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'Por episodio: malentendido → resolución → final dulce',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'es' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'fr', 'Romance sucrée',
  'Romance quotidienne légère et sucrée — chaque épisode a un petit conflit qui se résout par un moment doux. Parfait pour les comptes-matrices à publication quotidienne.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'Par épisode : malentendu → résolution → fin sucrée',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'fr' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'de', 'Süße Romanze',
  'Leichte, süße Alltagsromanze — jede Episode hat einen kleinen Konflikt, der sich in einem süßen Moment auflöst. Ideal für tägliche Matrix-Accounts.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'Pro Episode: Missverständnis → Auflösung → süßes Ende',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'de' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'it', 'Romance dolce',
  'Romance quotidiana leggera e dolce — ogni episodio ha un piccolo conflitto che si risolve in un momento dolce. Ottimo per account matrix a pubblicazione giornaliera.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'Per episodio: malinteso → risoluzione → finale dolce',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'it' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'pt', 'Romance doce',
  'Romance cotidiano leve e doce — cada episódio tem um pequeno conflito que se resolve em um momento doce. Ideal para contas matrix de postagem diária.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'Por episódio: mal-entendido → resolução → final doce',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'pt' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'nl', 'Zoete romance',
  'Lichte, zoete dagelijkse romance — elke aflevering heeft een klein conflict dat oplost in een zoet moment. Ideaal voor dagelijks-postende matrix-accounts.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'Per aflevering: misverstand → oplossing → zoet einde',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'nl' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'sv', 'Söt romantik',
  'Lätt och söt vardagsromantik — varje avsnitt har en liten konflikt som löses i en söt stund. Perfekt för dagligen-postande matriskonton.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'Per avsnitt: missförstånd → lösning → sött slut',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'sv' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'ja', '甘恋日常',
  '軽くて甘い日常恋愛 — 各話に小さなすれ違いが一つ、最後に甘く解決。日次更新のマトリクス運用向け。',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  '毎話: すれ違い → 解消 → 甘い結末',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'ja' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'ko', '달콤 로맨스',
  '가볍고 달콤한 일상 로맨스 — 회차마다 작은 갈등이 달콤한 순간으로 풀린다. 매일 업로드용 매트릭스 계정에 적합.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  '회차별: 오해 → 해소 → 달콤한 마무리',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'ko' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'ru', 'Сладкая романтика',
  'Лёгкая сладкая повседневная романтика — в каждой серии маленький конфликт, разрешающийся сладким моментом. Подходит для матрицы аккаунтов с ежедневным постингом.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'Каждая серия: недоразумение → разрешение → сладкий финал',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'ru' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'pl', 'Słodki romans',
  'Lekki, słodki codzienny romans — w każdym odcinku mały konflikt rozwiązujący się słodką chwilą. Świetny do kont matrycowych z codziennymi publikacjami.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'W każdym odcinku: nieporozumienie → rozwiązanie → słodkie zakończenie',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'pl' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'tr', 'Tatlı romans',
  'Hafif, tatlı günlük romantizm — her bölümde küçük bir çatışma tatlı bir anla çözülür. Günlük yayın yapan matris hesaplar için ideal.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'Her bölüm: yanlış anlaşılma → çözüm → tatlı son',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'tr' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'vi', 'Ngọt ngào hằng ngày',
  'Tình cảm ngọt ngào nhẹ nhàng trong đời thường — mỗi tập có mâu thuẫn nhỏ rồi tan biến thành khoảnh khắc ngọt ngào. Hợp với hệ tài khoản đăng hằng ngày.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'Mỗi tập: hiểu lầm → hóa giải → kết ngọt',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'vi' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'ar', 'رومانسية حلوة',
  'رومانسية يومية خفيفة وحلوة — في كل حلقة نزاع صغير ينتهي بلحظة حلوة. مناسب لحسابات النشر اليومي.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'كل حلقة: سوء فهم → حل → نهاية حلوة',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'ar' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'he', 'רומנטיקה מתוקה',
  'רומנטיקה יומיומית קלילה ומתוקה — בכל פרק עימות קטן שנפתר ברגע מתוק. מתאים לחשבונות מטריצה עם פרסום יומי.',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'כל פרק: אי-הבנה → פתרון → סיום מתוק',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'he' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'sweet-romance', 'th', 'รักหวานชื่นรายวัน',
  'เรื่องรักวันละนิดที่หวานและเบาสมอง — แต่ละตอนมีปมเล็ก ๆ ก่อนคลี่คลายเป็นช่วงเวลาน่ารัก เหมาะกับบัญชีโพสต์ทุกวัน',
  'sweet_love', 15, 90, '9:16', 'warm-soft',
  'douyin,kuaishou',
  'ในแต่ละตอน: เข้าใจผิด → คลี่คลาย → จบหวาน',
  'misunderstanding-reconciliation', 1, 0, 10, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'sweet-romance' AND `lang` = 'th' AND `is_system` = 1
);

-- ── ceo-drama ──
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'es', 'Romance del CEO',
  'Romance angustiosa basada en contrastes de identidad — CEO frío conoce a la heroína destinada, la reversión culmina en un final feliz. Género popular en apps de short drama.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'Contraste de identidades → angustia crece → reversión de la verdad → final feliz',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'es' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'fr', 'Romance du PDG',
  'Romance angoissée bâtie sur des contrastes d''identité — un PDG froid rencontre l''héroïne destinée, le retournement finit sur une fin heureuse. Genre prisé sur les apps de short-drama.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'Contraste d''identités → l''angoisse monte → retournement de vérité → happy end',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'fr' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'de', 'CEO-Romanze',
  'Schmerzliche Romanze auf Identitäts-Kontrasten — kalter CEO trifft die Bestimmte, die Wendung landet in einem Happy End. Hot-Genre auf Short-Drama-Apps.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'Identitätskontrast → Schmerz baut sich auf → Wahrheitsumkehr → Happy End',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'de' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'it', 'Amore col CEO',
  'Romance struggente costruita su contrasti d''identità — CEO freddo incontra l''eroina predestinata, il ribaltamento porta a un lieto fine. Genere caldo sulle app di short-drama.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'Contrasto d''identità → lo struggimento cresce → ribaltamento della verità → lieto fine',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'it' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'pt', 'Romance do CEO',
  'Romance angustiado baseado em contrastes de identidade — CEO frio encontra a heroína destinada, a reviravolta culmina em final feliz. Gênero quente nos apps de short drama.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'Contraste de identidades → angústia cresce → reviravolta da verdade → final feliz',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'pt' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'nl', 'CEO-romance',
  'Pijnlijke romance gebouwd op identiteitscontrasten — koele CEO ontmoet de voorbestemde heldin, de omkering eindigt in een happy end. Heet genre op short-drama-apps.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'Identiteitscontrast → leed groeit → waarheidsomkering → happy end',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'nl' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'sv', 'VD-romans',
  'Plågsam romantik byggd på identitetskontraster — kall VD möter den ödesbestämda hjältinnan, vändningen landar i ett lyckligt slut. Hett genre på short-drama-appar.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'Identitetskontrast → plåga växer → sanningsvändning → lyckligt slut',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'sv' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'ja', '総裁ラブストーリー',
  '身分差から生まれる切ない恋 — 冷徹な総裁が運命の女性と出会い、反転の末にHE。短劇アプリの人気ジャンル。',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  '身分差 → 切なさが積もる → 真実の反転 → ハッピーエンド',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'ja' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'ko', '재벌 로맨스',
  '신분 차로 시작되는 애절한 로맨스 — 냉철한 재벌이 운명의 여주를 만나고 반전을 거쳐 해피엔딩. 숏드라마 앱의 핫한 장르.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  '신분 대비 → 갈등 누적 → 진실의 반전 → 해피엔딩',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'ko' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'ru', 'Роман с боссом',
  'Болезненный роман на контрасте статусов — холодный CEO встречает свою судьбу, разворот ведёт к хеппи-энду. Горячий жанр в приложениях короткой драмы.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'Контраст статусов → боль нарастает → разворот правды → хеппи-энд',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'ru' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'pl', 'Romans z prezesem',
  'Pełen rozterek romans oparty na kontraście statusów — chłodny prezes spotyka przeznaczoną mu bohaterkę, zwrot kończy się happy endem. Gorący gatunek w aplikacjach z krótkimi dramami.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'Kontrast statusów → narastające cierpienie → zwrot ku prawdzie → szczęśliwe zakończenie',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'pl' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'tr', 'CEO aşkı',
  'Kimlik karşıtlıkları üzerine kurulu sancılı romantizm — soğuk CEO kaderi olan kadın kahramanla buluşur, dönüş mutlu sona iner. Short-drama uygulamalarında popüler tür.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'Kimlik karşıtlığı → ızdırap birikir → gerçeğin dönüşü → mutlu son',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'tr' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'vi', 'Tình yêu tổng tài',
  'Tình yêu day dứt trên nền chênh lệch thân phận — tổng tài lạnh lùng gặp nữ chính định mệnh, đảo chiều kết thúc viên mãn. Thể loại hot trên app short drama.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'Chênh lệch thân phận → day dứt dồn lại → đảo chiều sự thật → kết viên mãn',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'vi' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'ar', 'رومانسية الرئيس التنفيذي',
  'رومانسية مؤلمة قائمة على تباين الهويات — رئيس تنفيذي بارد يلتقي ببطلة القدر، والانعطافة تنتهي بنهاية سعيدة. نوع رائج على تطبيقات الدراما القصيرة.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'تباين الهوية → الألم يتراكم → انعطافة الحقيقة → نهاية سعيدة',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'ar' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'he', 'רומן מנכ"ל',
  'רומן כואב הנשען על ניגוד זהויות — מנכ"ל קר פוגש את גיבורת הגורל, ההיפוך מסתיים בסוף טוב. ז''אנר חם באפליקציות שורט-דרמה.',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'ניגוד זהויות → כאב מצטבר → היפוך אמת → סוף טוב',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'he' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'ceo-drama', 'th', 'รักท่านประธาน',
  'รักร้าวบนความต่างชนชั้น — ประธานเย็นชาพบนางเอกพรหมลิขิต พลิกเรื่องไปจบแบบแฮปปี้เอนดิ้ง แนวฮอตในแอปละครสั้น',
  'domineering', 20, 90, '9:16', 'cinematic-cool',
  'douyin,short-drama-app',
  'ปมชนชั้น → ความเจ็บสะสม → พลิกความจริง → จบแบบแฮปปี้',
  '3-act-reversal', 1, 0, 20, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'ceo-drama' AND `lang` = 'th' AND `is_system` = 1
);

-- ── mystery ──
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'es', 'Misterio que rompe la cabeza',
  'Pista por episodio — distracciones y reversiones mantienen al espectador enganchado. Funciona para fans del misterio de todas las edades.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'Sucede el caso → reunir pistas → distracción y reversión → verdad revelada',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'es' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'fr', 'Mystère retors',
  'Indice par épisode — leurres et retournements gardent les spectateurs accrochés. Marche pour les fans de mystère tous âges.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'Le crime → on rassemble les indices → leurre et retournement → vérité dévoilée',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'fr' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'de', 'Hirnverdrehender Krimi',
  'Pro Folge ein Hinweis — Ablenkungen und Wendungen halten Zuschauer am Haken. Funktioniert für Krimi-Fans jeden Alters.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'Fall passiert → Hinweise sammeln → Ablenkung und Wendung → Wahrheit enthüllt',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'de' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'it', 'Mistero rompicapo',
  'Un indizio per episodio — depistaggi e ribaltamenti tengono lo spettatore incollato. Funziona per appassionati di mistero di ogni età.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'Il caso accade → raccolta indizi → depistaggio e ribaltamento → verità svelata',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'it' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'pt', 'Mistério de quebrar a cabeça',
  'Pista por episódio — desvios e reviravoltas prendem o espectador. Funciona para fãs de mistério de todas as idades.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'O caso acontece → coleta de pistas → desvio e reviravolta → verdade revelada',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'pt' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'nl', 'Hersenkrakend mysterie',
  'Aanwijzing per aflevering — afleidingen en omkeringen houden de kijker geboeid. Werkt voor mystery-fans van alle leeftijden.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'Zaak gebeurt → aanwijzingen verzamelen → afleiding en omkering → waarheid onthuld',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'nl' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'sv', 'Hjärnvridande mysterium',
  'En ledtråd per avsnitt — vilseledningar och vändningar håller tittaren fast. Funkar för mysterie-fans i alla åldrar.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'Fallet sker → ledtrådar samlas → vilseledning och vändning → sanningen avslöjas',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'sv' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'ja', '頭脳系ミステリー',
  '毎話一つの手がかり — ミスリードと反転で視聴者を釘付け。全世代のミステリーファン向け。',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  '事件発生 → 手がかり収集 → ミスリードと反転 → 真相解明',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'ja' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'ko', '두뇌 미스터리',
  '회차마다 단서 하나 — 미끼와 반전으로 시청자를 사로잡는다. 전 연령 미스터리 팬용.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  '사건 발생 → 단서 수집 → 미끼와 반전 → 진실 공개',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'ko' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'ru', 'Загадочный мозголом',
  'Одна улика за серию — ложные следы и развороты держат зрителя. Подходит фанатам мистики любого возраста.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'Дело случается → сбор улик → ложный след и разворот → правда раскрыта',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'ru' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'pl', 'Łamigłówka kryminalna',
  'Po jednym tropie na odcinek — zwody i zwroty trzymają widza w napięciu. Działa na fanów kryminału w każdym wieku.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'Sprawa się dzieje → zbieranie tropów → zwód i zwrot → ujawnienie prawdy',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'pl' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'tr', 'Beyin yakan gizem',
  'Bölüm başına bir ipucu — yanıltmacalar ve dönüşlerle izleyiciyi tutar. Tüm yaşlardan gizem severlere hitap eder.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'Olay yaşanır → ipuçları toplanır → yanıltmaca ve dönüş → gerçek ortaya çıkar',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'tr' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'vi', 'Trinh thám hack não',
  'Mỗi tập một manh mối — đánh lạc hướng và bẻ ngoặt giữ chân khán giả. Phù hợp với fan trinh thám mọi lứa tuổi.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'Án xảy ra → thu manh mối → đánh lạc hướng và đảo chiều → sự thật phơi bày',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'vi' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'ar', 'غموض يشغل الذهن',
  'دليل في كل حلقة — تضليلات وانعطافات تُبقي المشاهد متشبثًا. مناسب لعشاق الغموض من كل الأعمار.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'وقوع القضية → جمع الأدلة → تضليل وانعطافة → كشف الحقيقة',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'ar' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'he', 'מסתורין ששובר את המוח',
  'רמז לכל פרק — הסחות והיפוכים שומרים על הצופה דרוך. מתאים לחובבי מסתורין בכל גיל.',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'המקרה קורה → איסוף רמזים → הסחה והיפוך → חשיפת האמת',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'he' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'mystery', 'th', 'ปริศนาหักมุมเขย่าสมอง',
  'เบาะแสตอนละหนึ่ง — กลลวงและพลิกผันทำให้คนดูติดหนึบ เหมาะกับแฟนแนวสืบสวนทุกวัย',
  'suspense', 12, 90, '9:16', 'dark-cinematic',
  'all-platforms',
  'เกิดคดี → รวบรวมเบาะแส → ลวงและพลิก → เปิดความจริง',
  '3-act-reversal', 1, 0, 30, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'mystery' AND `lang` = 'th' AND `is_system` = 1
);

-- ── time-travel ──
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'es', 'Reverso por viaje en el tiempo',
  'Protagonista moderno arrojado a una era pasada, usando conocimiento moderno para abrirse paso hacia la cima. Ritmo rápido y satisfacción inmediata.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'Salto temporal → adaptación → palanca de conocimiento moderno → ascenso gradual',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'es' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'fr', 'Revanche par voyage temporel',
  'Protagoniste moderne projeté dans une époque ancienne, utilisant son savoir moderne pour se hisser au sommet. Rythme rapide, satisfaction immédiate.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'Saut temporel → adaptation → levier du savoir moderne → ascension progressive',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'fr' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'de', 'Zeitreise-Comeback',
  'Moderner Protagonist landet in einer früheren Ära, nutzt modernes Wissen, um sich nach oben zu kämpfen. Hohe Schlagzahl und sofort befriedigend.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'Zeitsprung → Anpassung → Hebel modernes Wissen → schrittweiser Aufstieg',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'de' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'it', 'Riscatto in viaggio nel tempo',
  'Protagonista moderno catapultato in un''era antica, sfrutta la conoscenza moderna per arrampicarsi in alto. Ritmo serrato e soddisfazione immediata.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'Salto temporale → adattamento → leva del sapere moderno → ascesa graduale',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'it' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'pt', 'Volta por viagem no tempo',
  'Protagonista moderno jogado em uma era antiga, usando conhecimento moderno para subir ao topo. Ritmo rápido e satisfação imediata.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'Salto temporal → adaptação → alavanca do conhecimento moderno → ascensão gradual',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'pt' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'nl', 'Tijdreis-comeback',
  'Moderne hoofdpersoon belandt in een vroeger tijdperk, gebruikt moderne kennis om zich naar de top te knokken. Hoog tempo en directe voldoening.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'Tijdsprong → aanpassing → moderne kennis als hefboom → geleidelijke opmars',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'nl' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'sv', 'Tidsresekomeback',
  'Modern huvudperson kastas till en äldre era, använder modern kunskap för att kämpa sig till toppen. Högt tempo och direkt tillfredsställelse.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'Tidshopp → anpassning → modern kunskap som hävstång → gradvis uppgång',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'sv' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'ja', 'タイムスリップ逆転劇',
  '現代人が過去の時代へ飛ばされ、現代知識を武器にのし上がる。テンポが速くカタルシス直撃。',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  '時間移動 → 環境順応 → 現代知識の活用 → 段階的な逆転',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'ja' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'ko', '타임슬립 역전극',
  '현대인이 과거로 떨어져 현대 지식을 무기로 정상에 오른다. 템포가 빠르고 즉각적인 카타르시스.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  '시간 도약 → 환경 적응 → 현대 지식 레버리지 → 점진적 역전',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'ko' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'ru', 'Возвращение через путешествие во времени',
  'Современный герой попадает в прошлое и пользуется знаниями современности, чтобы пробиться наверх. Быстрый темп, мгновенное удовлетворение.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'Прыжок во времени → адаптация → рычаг современного знания → постепенный взлёт',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'ru' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'pl', 'Powrót dzięki podróży w czasie',
  'Współczesny bohater trafia w przeszłość i wykorzystuje współczesną wiedzę, by wedrzeć się na szczyt. Szybkie tempo i natychmiastowa satysfakcja.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'Skok w czasie → adaptacja → dźwignia współczesnej wiedzy → stopniowy wzlot',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'pl' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'tr', 'Zaman yolculuğu intikamı',
  'Modern bir karakter geçmişe fırlatılır, modern bilgiyle zirveye tırmanır. Hızlı tempo ve anında tatmin.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'Zaman sıçraması → uyum sağlama → modern bilgi avantajı → kademeli yükseliş',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'tr' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'vi', 'Trở lại nhờ xuyên không',
  'Nhân vật hiện đại bị đẩy về thời cổ, dùng kiến thức hiện đại vươn lên đỉnh. Nhịp nhanh và đã mắt tức thì.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'Xuyên không → thích nghi → tận dụng kiến thức hiện đại → từng bước trỗi dậy',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'vi' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'ar', 'نهضة عبر السفر في الزمن',
  'بطل معاصر يُلقى به إلى عصر قديم، يستخدم المعرفة الحديثة ليشق طريقه إلى القمة. إيقاع سريع ومتعة فورية.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'السفر في الزمن → التأقلم → الاستفادة من المعرفة الحديثة → صعود تدريجي',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'ar' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'he', 'היסט זמן ועלייה מחדש',
  'גיבור בן זמננו מוטח לעידן עתיק ומשתמש בידע מודרני כדי לטפס לפסגה. קצב מהיר וסיפוק מיידי.',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'קפיצת זמן → הסתגלות → מינוף ידע מודרני → עלייה הדרגתית',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'he' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'time-travel', 'th', 'ย้อนเวลาคืนชีพ',
  'พระเอกยุคใหม่ถูกพาไปในยุคโบราณ ใช้ความรู้สมัยใหม่ทยานสู่จุดสูงสุด ดำเนินเรื่องเร็วและสะใจทันที',
  'time_travel', 18, 90, '9:16', 'period-warm',
  'douyin,kuaishou',
  'ย้อนเวลา → ปรับตัว → ใช้ประโยชน์จากความรู้สมัยใหม่ → ค่อย ๆ พลิกชีวิต',
  'underdog-rise', 1, 0, 40, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'time-travel' AND `lang` = 'th' AND `is_system` = 1
);

-- ── urban-reversal ──
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'es', 'Subida del marginado urbano',
  'Protagonista oprimido despierta y arrolla a sus adversarios — pulsos densos de satisfacción, drama de liberación emocional.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'Oprimido → despertar → aplastar oponentes → final satisfactorio',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'es' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'fr', 'Ascension de l''opprimé urbain',
  'Protagoniste opprimé s''éveille et écrase ses adversaires — battements de satisfaction denses, drame de libération émotionnelle.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'Opprimé → éveil → écrasement des adversaires → paiement satisfaisant',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'fr' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'de', 'Aufstieg des urbanen Außenseiters',
  'Unterdrückter Protagonist erwacht und walzt die Gegner platt — dichte Befriedigungs-Beats, emotional entladendes Drama.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'Unterdrückt → Erwachen → Gegner zermalmen → befriedigender Höhepunkt',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'de' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'it', 'Riscossa del marginale urbano',
  'Protagonista oppresso si risveglia e travolge gli avversari — battute di soddisfazione densissime, dramma di sfogo emotivo.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'Oppresso → risveglio → schiacciare gli avversari → ricompensa appagante',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'it' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'pt', 'Ascensão do desfavorecido urbano',
  'Protagonista oprimido desperta e atropela os adversários — batidas densas de satisfação, drama de liberação emocional.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'Oprimido → despertar → esmagar oponentes → recompensa satisfatória',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'pt' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'nl', 'Opmars van de stedelijke underdog',
  'Onderdrukte hoofdpersoon ontwaakt en walst zijn tegenstanders plat — dichte voldoenings-beats, emotioneel ontladend drama.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'Onderdrukt → ontwaken → tegenstanders verpletteren → bevredigende afronding',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'nl' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'sv', 'Uppgång för stadens underdog',
  'Förtryckt huvudperson vaknar och kör över sina motståndare — täta tillfredsställelse-beats, känslomässigt utlösande drama.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'Förtryckt → uppvaknande → krossa motståndare → tillfredsställande final',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'sv' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'ja', '都市覚醒・爽快逆転',
  '虐げられた主人公が覚醒して敵を粉砕 — 爽快ビート密集、感情解放型ドラマ。',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  '抑圧 → 覚醒 → 敵を粉砕 → 爽快な決着',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'ja' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'ko', '도시형 사이다 역전',
  '억눌렸던 주인공이 각성해 적들을 짓밟는다 — 사이다 비트가 빽빽한 감정 해소형 드라마.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  '억압 → 각성 → 상대 제압 → 시원한 결말',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'ko' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'ru', 'Городское возвышение неудачника',
  'Угнетённый герой пробуждается и сметает противников — плотные сатисфакционные биты, драма эмоциональной разрядки.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'Подавление → пробуждение → разгром противников → удовлетворяющая развязка',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'ru' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'pl', 'Powstanie miejskiego underdoga',
  'Uciemiężony bohater budzi się i miażdży przeciwników — gęste beaty satysfakcji, dramat z emocjonalnym wyładowaniem.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'Ucisk → przebudzenie → zmiażdżenie przeciwników → satysfakcjonujący finał',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'pl' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'tr', 'Şehirde ezilenin yükselişi',
  'Ezilen kahraman uyanır ve rakiplerini yerle bir eder — yoğun tatmin vuruşları, duygusal boşalma draması.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'Ezilme → uyanış → rakipleri ezme → tatmin edici sonuç',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'tr' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'vi', 'Đô thị phất lên',
  'Nhân vật bị ức hiếp thức tỉnh và nghiền nát đối thủ — nhịp ''sướng'' dày đặc, drama giải tỏa cảm xúc.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'Bị áp bức → thức tỉnh → đè bẹp đối thủ → kết thỏa lòng',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'vi' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'ar', 'نهوض ابن المدينة المظلوم',
  'بطل مظلوم يستيقظ ويسحق خصومه — نبضات إرضاء كثيفة، ودراما إفراغ عاطفي.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'اضطهاد → يقظة → سحق الخصوم → ختام مُرضٍ',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'ar' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'he', 'עלייתו של האנדרדוג העירוני',
  'גיבור מדוכא מתעורר ומועך את יריביו — ביטים צפופים של סיפוק, דרמת שחרור רגשי.',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'דיכוי → התעוררות → מחיצת יריבים → סיום מספק',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'he' AND `is_system` = 1
);
INSERT INTO `w_drama_template`
  (`slug`, `lang`, `name`, `description`, `genre`,
   `default_episode_count`, `default_episode_duration`,
   `default_aspect_ratio`, `default_style_preset`,
   `default_target_platform`, `narrative_structure`,
   `emotional_template`, `is_system`, `uid`, `sort_order`, `status`)
SELECT 'urban-reversal', 'th', 'เมืองพลิกชะตา',
  'พระเอกที่ถูกกดดันตื่นรู้แล้วถล่มคู่ต่อสู้ราบ — จังหวะสะใจหนาแน่น ดราม่าระบายอารมณ์',
  'urban', 15, 90, '9:16', 'urban-cool',
  'all-platforms',
  'ถูกกดขี่ → ตื่นรู้ → บดขยี้คู่ต่อสู้ → จบแบบสะใจ',
  'underdog-rise', 1, 0, 50, 1
FROM DUAL WHERE NOT EXISTS (
  SELECT 1 FROM `w_drama_template` WHERE `slug` = 'urban-reversal' AND `lang` = 'th' AND `is_system` = 1
);

