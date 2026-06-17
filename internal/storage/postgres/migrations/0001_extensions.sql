CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    role          text NOT NULL CHECK (role IN ('user', 'expert')),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tags (
    id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE
);

INSERT INTO tags (name) VALUES ('ai-generated') ON CONFLICT DO NOTHING;
INSERT INTO tags (name) VALUES ('needs-manual') ON CONFLICT DO NOTHING;
