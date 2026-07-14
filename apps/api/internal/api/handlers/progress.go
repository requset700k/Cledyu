package handlers

import (
	"context"
	"time"

	"github.com/requset700k/cledyu/api/internal/store"
	"go.uber.org/zap"
)

// persistence는 핸들러가 쓰는 영속 계층이다(*store.Store 가 구현).
// nil 이면 종전과 동일하게 in-memory 로만 동작한다(로컬/CI, DB 미배포 환경).
type persistence interface {
	SaveProgress(ctx context.Context, sessionID string, p store.SessionProgress) error
	LoadProgress(ctx context.Context, sessionID string) (*store.SessionProgress, error)
	DeleteProgress(ctx context.Context, sessionID string) error
	UpsertUser(ctx context.Context, id, email, name, role string) error
	ListUsers(ctx context.Context, limit int) ([]store.User, error)
	ListCompletionsByUser(ctx context.Context, userID string) ([]store.Completion, error)
	SetUserRole(ctx context.Context, userID, role string) error
	RecordCompletion(ctx context.Context, userID, labID, sessionID string) error
	LeaderboardRows(ctx context.Context, since *time.Time) ([]store.LeaderboardRow, error)
	LeaderboardHidden(ctx context.Context, userID string) (bool, error)
	SetLeaderboardHidden(ctx context.Context, userID string, hidden bool) error
	ListInProgressLabsByUser(ctx context.Context, userID string) ([]store.InProgressLab, error)
	GetSubscription(ctx context.Context, userID string) (*store.Subscription, error)
	CreateCheckoutSession(ctx context.Context, session store.CheckoutSession) error
	GetCheckoutSession(ctx context.Context, id, userID string) (*store.CheckoutSession, error)
	CompleteCheckoutSession(ctx context.Context, id, userID string) (*store.Subscription, error)
}

// dbTimeout은 요청 경로에서 허용하는 DB 왕복 상한이다.
const dbTimeout = 5 * time.Second

// ── stepStore 영속화 ─────────────────────────────────────────────────────────
//
// 전략: in-memory 맵을 캐시로 유지하고
//   - 변경(dirty) 시 스냅샷을 DB에 write-through (잠금 밖, 동기)
//   - 캐시 미스 시 DB에서 적재(load-on-miss) — API 재시작 후 진행 상태 복원
// DB 실패는 경고 로그만 남긴다 — 실행 중인 프로세스에서는 캐시가 진실의 원천이다.

// withSession은 세션 엔트리를 잠금 하에 fn 에 전달한다. 캐시 미스 시 DB 적재를 시도한다.
// fn 이 true(dirty)를 반환하면 변경 스냅샷을 DB에 기록한다. 세션이 어디에도 없으면 false.
func (st *stepStore) withSession(sessionID string, fn func(ss *sessionSteps) bool) bool {
	st.mu.Lock()
	ss, ok := st.m[sessionID]
	if !ok && st.db != nil {
		// load-on-miss — 잠금을 쥔 채 1회 조회(단일 인스턴스·저트래픽 전제, 수 ms).
		ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
		p, err := st.db.LoadProgress(ctx, sessionID)
		cancel()
		if err != nil {
			st.logWarn("진행 상태 DB 적재 실패", sessionID, err)
		} else if p != nil {
			ss = fromStoreProgress(p)
			st.m[sessionID] = ss
			ok = true
		}
	}
	if !ok {
		st.mu.Unlock()
		return false
	}

	dirty := fn(ss)
	var snap *store.SessionProgress
	if dirty && st.db != nil {
		snap = toStoreProgress(ss) // 깊은 복사 — 잠금 해제 후 안전하게 직렬화
	}
	st.mu.Unlock()

	if snap != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
		defer cancel()
		if err := st.db.SaveProgress(ctx, sessionID, *snap); err != nil {
			st.logWarn("진행 상태 DB 저장 실패", sessionID, err)
		}
	}
	return true
}

// put은 새 세션 진행 상태를 등록하고 DB에 기록한다(CreateSession).
func (st *stepStore) put(sessionID string, ss *sessionSteps) {
	st.mu.Lock()
	st.m[sessionID] = ss
	var snap *store.SessionProgress
	if st.db != nil {
		snap = toStoreProgress(ss)
	}
	st.mu.Unlock()

	if snap != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
		defer cancel()
		if err := st.db.SaveProgress(ctx, sessionID, *snap); err != nil {
			st.logWarn("진행 상태 DB 저장 실패", sessionID, err)
		}
	}
}

// take는 세션 진행 상태를 캐시·DB 양쪽에서 제거하고 마지막 상태를 반환한다(DeleteSession).
// 캐시 미스 시 DB에서 읽어 반환한다 — 재시작 직후 삭제에서도 lab_abandoned 판정이 가능하다.
func (st *stepStore) take(sessionID string) (*sessionSteps, bool) {
	st.mu.Lock()
	ss, ok := st.m[sessionID]
	delete(st.m, sessionID)
	st.mu.Unlock()

	if st.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
		defer cancel()
		if !ok {
			if p, err := st.db.LoadProgress(ctx, sessionID); err == nil && p != nil {
				ss, ok = fromStoreProgress(p), true
			}
		}
		if err := st.db.DeleteProgress(ctx, sessionID); err != nil {
			st.logWarn("진행 상태 DB 삭제 실패", sessionID, err)
		}
	}
	return ss, ok
}

// remove는 take 의 반환값 없는 버전이다(reaper 등 정리 경로).
func (st *stepStore) remove(sessionID string) { _, _ = st.take(sessionID) }

func (st *stepStore) logWarn(msg, sessionID string, err error) {
	if st.log != nil {
		st.log.Warn(msg, zap.String("session_id", sessionID), zap.Error(err))
	}
}

// ── store 타입 변환 ──────────────────────────────────────────────────────────

// toStoreProgress는 영속 스냅샷을 만든다(슬라이스 깊은 복사 — 잠금 밖 직렬화 안전).
func toStoreProgress(ss *sessionSteps) *store.SessionProgress {
	p := &store.SessionProgress{
		LabID:       ss.LabID,
		UserID:      ss.UserID,
		CurrentStep: ss.CurrentStep,
		Steps:       make([]store.StepProgress, 0, len(ss.Steps)),
	}
	for i := range ss.Steps {
		st := ss.Steps[i]
		sp := store.StepProgress{
			StepID:    st.StepID,
			Status:    st.Status,
			Attempts:  st.Attempts,
			HintLevel: st.HintLevel,
		}
		for _, ck := range st.Checks {
			sp.Checks = append(sp.Checks, store.CheckOutcome{Type: ck.Type, Passed: ck.Passed, Detail: ck.Detail})
		}
		p.Steps = append(p.Steps, sp)
	}
	return p
}

func fromStoreProgress(p *store.SessionProgress) *sessionSteps {
	ss := &sessionSteps{
		LabID:       p.LabID,
		UserID:      p.UserID,
		CurrentStep: p.CurrentStep,
		Steps:       make([]stepState, 0, len(p.Steps)),
	}
	for _, sp := range p.Steps {
		st := stepState{
			StepID:    sp.StepID,
			Status:    sp.Status,
			Attempts:  sp.Attempts,
			HintLevel: sp.HintLevel,
		}
		for _, ck := range sp.Checks {
			st.Checks = append(st.Checks, checkOutcome{Type: ck.Type, Passed: ck.Passed, Detail: ck.Detail})
		}
		ss.Steps = append(ss.Steps, st)
	}
	return ss
}
