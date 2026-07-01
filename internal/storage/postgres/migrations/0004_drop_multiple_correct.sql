-- 0004_drop_multiple_correct.sql
-- multiple_correct is fully derivable from len(answers) > 1.
-- Drop the redundant column; the value is recomputed in the domain layer (spec §3.3, §5).
ALTER TABLE questions DROP COLUMN IF EXISTS multiple_correct;
