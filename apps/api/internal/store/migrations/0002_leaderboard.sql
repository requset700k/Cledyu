-- 리더보드 노출 옵트아웃 — 기본 노출(false). 학습자가 명시적으로 숨길 수 있다.
ALTER TABLE users ADD COLUMN IF NOT EXISTS leaderboard_hidden boolean NOT NULL DEFAULT false;
