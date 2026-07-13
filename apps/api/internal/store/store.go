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

// User는 users 테이블 한 행이다(관리자 콘솔 조회용).
type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	LastLoginAt time.Time `json:"last_login_at"`
}

// ListUsers는 최근 로그인 순으로 유저 목록을 반환한다(관리자 콘솔). limit<=0 이면 50.
func (s *Store) ListUsers(ctx context.Context, limit int) ([]User, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, email, name, role, created_at, last_login_at
		FROM users ORDER BY last_login_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]User, 0, limit)
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt, &u.LastLoginAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

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

// Completion은 lab_completions 한 행이다(유저 활동 조회용).
type Completion struct {
	LabID       string    `json:"lab_id"`
	SessionID   string    `json:"session_id"`
	CompletedAt time.Time `json:"completed_at"`
}

// InProgressLab는 사용자가 진행 중인 랩과 이어갈 세션 ID다.
type InProgressLab struct {
	LabID     string `json:"lab_id"`
	SessionID string `json:"session_id"`
}

// ListCompletionsByUser는 유저의 랩 완료 이력을 최신 순으로 반환한다(관리자 활동 조회).
func (s *Store) ListCompletionsByUser(ctx context.Context, userID string) ([]Completion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT lab_id, session_id, completed_at
		FROM lab_completions WHERE user_id = $1 ORDER BY completed_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list completions: %w", err)
	}
	defer rows.Close()

	out := make([]Completion, 0)
	for rows.Next() {
		var c Completion
		if err := rows.Scan(&c.LabID, &c.SessionID, &c.CompletedAt); err != nil {
			return nil, fmt.Errorf("scan completion: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListInProgressLabsByUser는 유저가 진행기록(session_progress)을 가진 lab_id/session_id 목록을 반환한다.
// 완료된 랩도 진행기록이 남아 있을 수 있으므로, 호출부는 완료 여부를 먼저 판정한 뒤
// 이 목록을 'in_progress' 후보로 사용한다.
func (s *Store) ListInProgressLabsByUser(ctx context.Context, userID string) ([]InProgressLab, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (lab_id) lab_id, session_id
		FROM session_progress
		WHERE user_id = $1
		ORDER BY lab_id, updated_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list in-progress labs: %w", err)
	}
	defer rows.Close()

	out := make([]InProgressLab, 0)
	for rows.Next() {
		var row InProgressLab
		if err := rows.Scan(&row.LabID, &row.SessionID); err != nil {
			return nil, fmt.Errorf("scan in-progress lab: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// LeaderboardRow는 리더보드 집계용 완료 1건이다(유저 표시명 포함).
type LeaderboardRow struct {
	UserID      string    `json:"user_id"`
	Name        string    `json:"name"`
	LabID       string    `json:"lab_id"`
	CompletedAt time.Time `json:"completed_at"`
}

// LeaderboardRows는 옵트아웃하지 않은 유저의 랩 완료 행을 반환한다.
// since 가 nil 이 아니면 completed_at >= *since 만 포함한다(최근 급상승 윈도우).
// 난이도 가중은 호출부(핸들러)가 in-memory 콘텐츠로 수행하므로 여기선 raw 행만 돌려준다.
// users 와 INNER JOIN — 완료 기록은 로그인(미러 생성)을 전제로 한다.
func (s *Store) LeaderboardRows(ctx context.Context, since *time.Time) ([]LeaderboardRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.user_id, u.name, c.lab_id, c.completed_at
		FROM lab_completions c
		JOIN users u ON u.id = c.user_id
		WHERE u.leaderboard_hidden = false
		  AND ($1::timestamptz IS NULL OR c.completed_at >= $1)`, since)
	if err != nil {
		return nil, fmt.Errorf("leaderboard rows: %w", err)
	}
	defer rows.Close()

	out := make([]LeaderboardRow, 0)
	for rows.Next() {
		var r LeaderboardRow
		if err := rows.Scan(&r.UserID, &r.Name, &r.LabID, &r.CompletedAt); err != nil {
			return nil, fmt.Errorf("scan leaderboard row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetLeaderboardHidden은 유저의 리더보드 노출 여부를 갱신한다(옵트아웃 토글).
func (s *Store) SetLeaderboardHidden(ctx context.Context, userID string, hidden bool) error {
	// upsert로 행을 보장한다 — 로그인 직후 UpsertUser(비동기)가 아직 미러 행을 만들기 전이라도
	// 옵트아웃이 유실되지 않도록. UPDATE 만 쓰면 0행 갱신 후 200 을 반환하고, 뒤늦은 upsert 가
	// 기본값 leaderboard_hidden=false 로 행을 만들어 숨김이 풀린다(이름 공개 노출).
	// UpsertUser 의 ON CONFLICT 는 leaderboard_hidden 을 건드리지 않으므로 순서와 무관하게 보존된다.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO users (id, leaderboard_hidden)
		VALUES ($1, $2)
		ON CONFLICT (id) DO UPDATE SET leaderboard_hidden = EXCLUDED.leaderboard_hidden`,
		userID, hidden); err != nil {
		return fmt.Errorf("set leaderboard hidden: %w", err)
	}
	return nil
}

// SetUserRole은 users 미러의 역할 스냅샷을 갱신한다(Keycloak 역할 변경 직후 즉시 반영용).
// 해당 유저가 아직 미러에 없으면(로그인 전) 영향 없음 — 다음 로그인 시 upsert 된다.
func (s *Store) SetUserRole(ctx context.Context, userID, role string) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE users SET role = $2 WHERE id = $1`, userID, role); err != nil {
		return fmt.Errorf("set user role: %w", err)
	}
	return nil
}
