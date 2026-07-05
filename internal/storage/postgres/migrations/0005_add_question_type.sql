-- 0005_add_question_type.sql
-- Explicit MC/FR discriminator. Inferred once at extraction time from
-- len(choices); mirrors the choice_labeling precedent. Editable by experts.
ALTER TABLE questions
    ADD COLUMN IF NOT EXISTS question_type text NOT NULL DEFAULT 'multiple_choice'
    CHECK (question_type IN ('multiple_choice', 'free_response'));

-- Backfill: existing non-error rows with empty choices are free-response.
-- Error rows (status = 'error') are failure placeholders, not FR questions;
-- they keep the default 'multiple_choice'.
UPDATE questions
SET question_type = 'free_response'
WHERE choices = '[]'::jsonb AND status <> 'error';
