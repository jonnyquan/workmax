-- Rename w_use_cases → w_use_case. Final pass of the snake_case
-- + singular-form normalisation. Marketing surface table — one row
-- per case study — fits the same singular convention as
-- w_blog / w_blog_category / w_email_template.

RENAME TABLE `w_use_cases` TO `w_use_case`;
