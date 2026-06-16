-- 앱 측 영속 스키마 v1 — 유저 미러 + 실습 진행 상태 + 랩 완료 이력.
-- 신원의 원본은 Keycloak(cledyu-learn realm)이고 users 는 앱 데이터 조인용 미러다.

CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,                       -- Keycloak sub
    email         TEXT NOT NULL DEFAULT '',
    name          TEXT NOT NULL DEFAULT '',
    role          TEXT NOT NULL DEFAULT 'student',        -- 로그인 시점의 realm 역할 스냅샷
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz NOT NULL DEFAULT now()
);

-- 세션 헤더 — in-memory stepStore 의 영속본. API 재시작 시 진행 상태 복원의 원본.
CREATE TABLE IF NOT EXISTS session_progress (
    session_id   TEXT PRIMARY KEY,
    lab_id       TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    current_step INT  NOT NULL DEFAULT 0,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- 스텝별 진행 — checks 는 검증엔진 체크 상세(JSONB, 실패 사유 표시용).
CREATE TABLE IF NOT EXISTS session_steps (
    session_id TEXT NOT NULL REFERENCES session_progress(session_id) ON DELETE CASCADE,
    step_id    INT  NOT NULL,
    status     TEXT NOT NULL,
    attempts   INT  NOT NULL DEFAULT 0,
    hint_level INT  NOT NULL DEFAULT 0,
    checks     JSONB,
    PRIMARY KEY (session_id, step_id)
);

-- 랩 완료 이력 — (user, lab) 최초 완료 기준. 수료증·배지·리더보드의 원천.
CREATE TABLE IF NOT EXISTS lab_completions (
    user_id      TEXT NOT NULL,
    lab_id       TEXT NOT NULL,
    session_id   TEXT NOT NULL,
    completed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, lab_id)
);

CREATE INDEX IF NOT EXISTS idx_session_progress_user ON session_progress(user_id);
CREATE INDEX IF NOT EXISTS idx_lab_completions_lab ON lab_completions(lab_id);
