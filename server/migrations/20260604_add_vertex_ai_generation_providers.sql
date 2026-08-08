-- Register disabled Vertex AI provider rows for Veo 3.1 and Gemini image models.
-- Provider type names represent only the provider/channel.
-- `model` represents the business model and lets multiple provider
-- implementations participate in the same model candidate pool. Operators
-- must set extra_config.vertexProjectId and one credential source (API key,
-- ADC, or extra_config.vertexCredentialsJson) before enabling these rows.
 
INSERT INTO `w_generator_provider`
  (`name`, `type`, `media_type`, `enabled`, `is_default`, `priority`,
   `endpoint`, `api_key`, `model`, `daily_quota`, `monthly_quota`,
   `concurrent_limit`, `extra_config`, `description`, `created_at`, `updated_at`)
SELECT
  'vertex_veo31_video',
  'vertex',
  'video',
  0,
  0,
  80,
  'https://us-central1-aiplatform.googleapis.com',
  '',
  'veo-3.1-generate-001',
  0,
  0,
  2,
  JSON_OBJECT('vertexLocation', 'us-central1', 'pollInterval', 10, 'maxWaitSeconds', 900),
  'Vertex AI Veo 3.1 video provider',
  NOW(),
  NOW()
WHERE NOT EXISTS (
  SELECT 1 FROM `w_generator_provider`
   WHERE `name` = 'vertex_veo31_video'
     AND `deleted_at` IS NULL
);

INSERT INTO `w_generator_provider`
  (`name`, `type`, `media_type`, `enabled`, `is_default`, `priority`,
   `endpoint`, `api_key`, `model`, `daily_quota`, `monthly_quota`,
   `concurrent_limit`, `extra_config`, `description`, `created_at`, `updated_at`)
SELECT
  'vertex_veo31_fast_video',
  'vertex',
  'video',
  0,
  0,
  79,
  'https://us-central1-aiplatform.googleapis.com',
  '',
  'veo-3.1-fast-generate-001',
  0,
  0,
  2,
  JSON_OBJECT('vertexLocation', 'us-central1', 'pollInterval', 10, 'maxWaitSeconds', 900),
  'Vertex AI Veo 3.1 Fast video provider',
  NOW(),
  NOW()
WHERE NOT EXISTS (
  SELECT 1 FROM `w_generator_provider`
   WHERE `name` = 'vertex_veo31_fast_video'
     AND `deleted_at` IS NULL
);

INSERT INTO `w_generator_provider`
  (`name`, `type`, `media_type`, `enabled`, `is_default`, `priority`,
   `endpoint`, `api_key`, `model`, `daily_quota`, `monthly_quota`,
   `concurrent_limit`, `extra_config`, `description`, `created_at`, `updated_at`)
SELECT
  'vertex_gemini31_flash_image',
  'vertex',
  'image',
  0,
  0,
  72,
  'https://aiplatform.googleapis.com',
  '',
  'gemini-3.1-flash-image-preview',
  0,
  0,
  5,
  JSON_OBJECT('vertexLocation', 'global'),
  'Vertex AI Gemini 3.1 Flash Image provider',
  NOW(),
  NOW()
WHERE NOT EXISTS (
  SELECT 1 FROM `w_generator_provider`
   WHERE `name` = 'vertex_gemini31_flash_image'
     AND `deleted_at` IS NULL
);

INSERT INTO `w_generator_provider`
  (`name`, `type`, `media_type`, `enabled`, `is_default`, `priority`,
   `endpoint`, `api_key`, `model`, `daily_quota`, `monthly_quota`,
   `concurrent_limit`, `extra_config`, `description`, `created_at`, `updated_at`)
SELECT
  'vertex_gemini3_pro_image',
  'vertex',
  'image',
  0,
  0,
  71,
  'https://aiplatform.googleapis.com',
  '',
  'gemini-3-pro-image-preview',
  0,
  0,
  5,
  JSON_OBJECT('vertexLocation', 'global'),
  'Vertex AI Gemini 3 Pro Image provider',
  NOW(),
  NOW()
WHERE NOT EXISTS (
  SELECT 1 FROM `w_generator_provider`
   WHERE `name` = 'vertex_gemini3_pro_image'
     AND `deleted_at` IS NULL
);

-- Normalize existing rows to provider-only type names.
-- Model identity remains in `model`; routing still accepts the old values as
-- aliases for backward compatibility.
UPDATE `w_generator_provider`
   SET `type` = 'gemini',
       `updated_at` = NOW()
 WHERE `type` IN ('nanobanana', 'nanobanana2', 'nanobanana_pro', 'gemini_gemini_image', 'gemini_veo', 'veo31')
   AND `deleted_at` IS NULL;

UPDATE `w_generator_provider`
   SET `type` = 'vertex',
       `updated_at` = NOW()
 WHERE `type` IN ('vertex_gemini_image', 'vertex_veo')
   AND `deleted_at` IS NULL;

UPDATE `w_generator_provider`
   SET `type` = 'kling',
       `updated_at` = NOW()
 WHERE `type` = 'kling26'
   AND `deleted_at` IS NULL;

UPDATE `w_generator_provider`
   SET `type` = 'sora',
       `updated_at` = NOW()
 WHERE `type` = 'sora2'
   AND `deleted_at` IS NULL;

UPDATE `w_generator_provider`
   SET `type` = 'seedance',
       `updated_at` = NOW()
 WHERE `type` = 'seedance2'
   AND `deleted_at` IS NULL;

UPDATE `w_generator_provider`
   SET `type` = 'minimax',
       `updated_at` = NOW()
 WHERE `type` IN ('minimax_video', 'minimax_hailuo_02')
   AND `deleted_at` IS NULL;

UPDATE `w_generator_provider`
   SET `type` = 'openai',
       `updated_at` = NOW()
 WHERE `type` = 'gpt_image_2'
   AND `deleted_at` IS NULL;

UPDATE `w_generator_provider`
   SET `type` = 'replicate',
       `updated_at` = NOW()
 WHERE `type` = 'hunyuan_3d_31'
   AND `deleted_at` IS NULL;
