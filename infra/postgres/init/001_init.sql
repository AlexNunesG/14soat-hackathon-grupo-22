-- Delivery 1 (auth-service): users table + pgcrypto for gen_random_uuid().
--
-- This file is auto-applied by Postgres's docker-entrypoint-initdb.d on
-- first container boot (see infra/docker-compose.yml). It is the DB
-- creation script deliverable called for by the hackathon brief.
--
-- Delivery 2 (video-service) appends its own `videos` table + indexes +
-- `video_status` enum to this same file (per .ai-agents/PLAN.md's data
-- model) rather than introducing a separate 002_*.sql, since both tables
-- are foundational schema needed before any service can run end to end.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email           VARCHAR(255) NOT NULL UNIQUE,
    password_hash   VARCHAR(255) NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
