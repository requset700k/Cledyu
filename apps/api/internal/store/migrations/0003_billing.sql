-- 결제/구독 MVP 스키마.
-- 실제 PG 승인/웹훅 연동 전에도 앱 내부에서 현재 플랜과 checkout 시도 이력을 추적한다.

CREATE TABLE IF NOT EXISTS subscriptions (
    user_id            TEXT PRIMARY KEY,
    plan_id            TEXT NOT NULL,
    status             TEXT NOT NULL, -- free | active | past_due | canceled
    current_period_end timestamptz,
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS checkout_sessions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    plan_id      TEXT NOT NULL,
    provider     TEXT NOT NULL,
    status       TEXT NOT NULL, -- pending | confirmed | completed | expired | canceled
    checkout_url TEXT NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_checkout_sessions_user_created
    ON checkout_sessions(user_id, created_at DESC);
