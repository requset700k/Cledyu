-- checkout 생성 당시 금액을 보존한다.
-- Toss success redirect의 amount 검증은 현재 plan 가격이 아니라 checkout 시점의 금액 스냅샷과 대조해야 한다.
ALTER TABLE checkout_sessions
    ADD COLUMN IF NOT EXISTS amount_krw INTEGER NOT NULL DEFAULT 0;
