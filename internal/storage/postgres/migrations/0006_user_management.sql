-- 0006_user_management.sql
-- Admin RBAC: widen role CHECK, add active + token_version for stateless JWT
-- invalidation, and make verified_by ON DELETE SET NULL so deleting a user who
-- verified questions preserves those questions (null attribution).
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('user', 'expert', 'admin'));
ALTER TABLE users ADD COLUMN IF NOT EXISTS active boolean NOT NULL DEFAULT true,
                           ADD COLUMN IF NOT EXISTS token_version bigint NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_users_role_active ON users(role, active);

-- verified_by: NO ACTION -> ON DELETE SET NULL (preserve question, null attribution)
ALTER TABLE questions DROP CONSTRAINT IF EXISTS questions_verified_by_fkey;
ALTER TABLE questions ADD CONSTRAINT questions_verified_by_fkey
    FOREIGN KEY (verified_by) REFERENCES users(id) ON DELETE SET NULL;
