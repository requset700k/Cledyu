CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    lab_id TEXT NOT NULL,
    namespace TEXT NOT NULL UNIQUE,
    vm_name TEXT NOT NULL DEFAULT 'session-vm',
    status TEXT NOT NULL CHECK (status IN ('provisioning', 'ready', 'failed', 'ended')),
    started_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX sessions_user_status_idx ON sessions (user_id, status);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE session_steps (
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    step_id INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'active', 'validating', 'passed', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    passed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, step_id)
);

CREATE INDEX session_steps_status_idx ON session_steps (status);

CREATE TABLE validation_attempts (
    id BIGSERIAL PRIMARY KEY,
    session_id TEXT NOT NULL,
    step_id INTEGER NOT NULL,
    trace_id TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('validating', 'passed', 'failed')),
    checks_result JSONB NOT NULL DEFAULT '[]'::jsonb,
    message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (session_id, step_id) REFERENCES session_steps (session_id, step_id) ON DELETE CASCADE
);

CREATE INDEX validation_attempts_session_step_idx ON validation_attempts (session_id, step_id, created_at DESC);
