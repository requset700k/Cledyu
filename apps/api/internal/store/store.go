// Package store는 PostgreSQL 영속 계층이다 — 유저 미러, 실습 진행 상태, 랩 완료 이력.
// DSN 미설정 시 호출부(main)가 Store 를 nil 로 두고, 핸들러는 종전처럼 in-memory 로만 동작한다.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

type Store struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

// logField는 migrate.go 와 공유하는 zap 필드 헬퍼다.
func logField(key, val string) zap.Field { return zap.String(key, val) }

// Open은 풀을 만들고 연결을 확인한 뒤 마이그레이션을 적용한다.
func Open(ctx context.Context, dsn string, log *zap.Logger) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse db dsn: %w", err)
	}
	cfg.MaxConns = 8 // 단일 API 인스턴스 + 저빈도 쓰기 — 작은 풀로 충분

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create db pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	s := &Store{pool: pool, log: log}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() { s.pool.Close() }

// ── users ───────────────────────────────────────────────────────────────────

// UpsertUser는 로그인(콜백/리프레시) 시점에 Keycloak 신원을 앱 DB로 미러링한다.
// 재로그인 시 프로필·역할 스냅샷과 last_login_at 을 갱신한다.
func (s *Store) UpsertUser(ctx context.Context, id, email, name, role string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (id, email, name, role)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET
			email = EXCLUDED.email,
			name = EXCLUDED.name,
			role = EXCLUDED.role,
			last_login_at = now()`,
		id, email, name, role)
	if err != nil {
		return fmt.Errorf("upsert user: %w", err)
	}
	return nil
}

// ── session progress ─────────────────────────────────────────────────────────

// CheckOutcome은 검증엔진 체크 상세다(handlers.checkOutcome 과 대응, JSONB 직렬화).
type CheckOutcome struct {
	Type   string `json:"type"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// StepProgress는 스텝 한 개의 영속 상태다.
type StepProgress struct {
	StepID    int
	Status    string
	Attempts  int
	HintLevel int
	Checks    []CheckOutcome
}

// SessionProgress는 세션 헤더 + 스텝 전체의 영속 스냅샷이다.
type SessionProgress struct {
	LabID       string
	UserID      string
	CurrentStep int
	Steps       []StepProgress
}

// SaveProgress는 세션 진행 스냅샷 전체를 upsert 한다(헤더 + 스텝, 단일 트랜잭션).
// 스텝 수가 한 자릿수라 전량 upsert 가 부분 갱신 추적보다 단순하고 충분히 싸다.
func (s *Store) SaveProgress(ctx context.Context, sessionID string, p SessionProgress) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin save progress: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		INSERT INTO session_progress (session_id, lab_id, user_id, current_step, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (session_id) DO UPDATE SET
			current_step = EXCLUDED.current_step,
			updated_at = now()`,
		sessionID, p.LabID, p.UserID, p.CurrentStep); err != nil {
		return fmt.Errorf("upsert session_progress: %w", err)
	}

	for _, st := range p.Steps {
		var checks []byte
		if len(st.Checks) > 0 {
			if checks, err = json.Marshal(st.Checks); err != nil {
				return fmt.Errorf("marshal checks: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO session_steps (session_id, step_id, status, attempts, hint_level, checks)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (session_id, step_id) DO UPDATE SET
				status = EXCLUDED.status,
				attempts = EXCLUDED.attempts,
				hint_level = EXCLUDED.hint_level,
				checks = EXCLUDED.checks`,
			sessionID, st.StepID, st.Status, st.Attempts, st.HintLevel, checks); err != nil {
			return fmt.Errorf("upsert session_steps: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// LoadProgress는 세션 진행 스냅샷을 읽는다. 없으면 (nil, nil).
// API 재시작 후 첫 접근(load-on-miss)에서 in-memory 캐시를 복원하는 데 쓰인다.
func (s *Store) LoadProgress(ctx context.Context, sessionID string) (*SessionProgress, error) {
	var p SessionProgress
	err := s.pool.QueryRow(ctx,
		`SELECT lab_id, user_id, current_step FROM session_progress WHERE session_id = $1`,
		sessionID,
	).Scan(&p.LabID, &p.UserID, &p.CurrentStep)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load session_progress: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT step_id, status, attempts, hint_level, checks
		FROM session_steps WHERE session_id = $1 ORDER BY step_id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load session_steps: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var st StepProgress
		var checks []byte
		if err := rows.Scan(&st.StepID, &st.Status, &st.Attempts, &st.HintLevel, &checks); err != nil {
			return nil, fmt.Errorf("scan session_step: %w", err)
		}
		if len(checks) > 0 {
			if err := json.Unmarshal(checks, &st.Checks); err != nil {
				return nil, fmt.Errorf("unmarshal checks: %w", err)
			}
		}
		p.Steps = append(p.Steps, st)
	}
	return &p, rows.Err()
}

// DeleteProgress는 세션 진행 기록을 삭제한다(steps 는 FK cascade).
func (s *Store) DeleteProgress(ctx context.Context, sessionID string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM session_progress WHERE session_id = $1`, sessionID); err != nil {
		return fmt.Errorf("delete session_progress: %w", err)
	}
	return nil
}

// ── lab completions ──────────────────────────────────────────────────────────

// RecordCompletion은 (user, lab) 최초 완료를 기록한다. 재완료는 무시(idempotent).
func (s *Store) RecordCompletion(ctx context.Context, userID, labID, sessionID string) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO lab_completions (user_id, lab_id, session_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, lab_id) DO NOTHING`,
		userID, labID, sessionID); err != nil {
		return fmt.Errorf("record completion: %w", err)
	}
	return nil
}
