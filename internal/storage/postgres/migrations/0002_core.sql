CREATE TABLE IF NOT EXISTS sessions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    duration_seconds int NOT NULL CHECK (duration_seconds > 0),
    buffer_seconds   int NOT NULL CHECK (buffer_seconds >= 0),
    started_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    status           text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed', 'expired'))
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id, started_at DESC);

CREATE TABLE IF NOT EXISTS images (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id          uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    original            bytea,
    enhanced            bytea,
    mime                text NOT NULL,
    width               int,
    height              int,
    verification_report jsonb,
    extraction_error    jsonb,
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_images_session ON images(session_id, created_at);

CREATE TABLE IF NOT EXISTS questions (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    number              int NOT NULL,
    question            text NOT NULL DEFAULT '',
    question_normalized text NOT NULL DEFAULT '',
    question_hash       text NOT NULL UNIQUE,
    multiple_correct    boolean NOT NULL DEFAULT false,
    choices             jsonb NOT NULL DEFAULT '[]',
    answers             jsonb NOT NULL DEFAULT '[]',
    choice_labeling     text NOT NULL DEFAULT 'letter' CHECK (choice_labeling IN ('letter', 'number')),
    confidence          numeric(3,2) NOT NULL DEFAULT 0,
    explanation         text NOT NULL DEFAULT '',
    embedding           vector(1536),
    status              text NOT NULL DEFAULT 'moderation' CHECK (status IN ('moderation', 'verified', 'error')),
    verified_at         timestamptz,
    verified_by         uuid REFERENCES users(id),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_questions_status ON questions(status);
CREATE INDEX IF NOT EXISTS idx_questions_embedding ON questions USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);

CREATE TABLE IF NOT EXISTS session_questions (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id            uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    image_id              uuid NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    question_id           uuid NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    extracted_number      int NOT NULL,
    extracted_confidence  numeric(3,2) NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE(session_id, image_id, question_id)
);
CREATE INDEX IF NOT EXISTS idx_session_questions_image ON session_questions(image_id);
CREATE INDEX IF NOT EXISTS idx_session_questions_session ON session_questions(session_id);

CREATE TABLE IF NOT EXISTS question_tags (
    question_id uuid NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    tag_id      uuid NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (question_id, tag_id)
);

CREATE TABLE IF NOT EXISTS jobs (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    image_id    uuid NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    session_id  uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'done', 'failed')),
    attempts    int NOT NULL DEFAULT 0,
    last_error  text,
    queued_at   timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_jobs_status_queued ON jobs(status, queued_at);
