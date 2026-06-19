package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/events"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
	"github.com/requset700k/cledyu/api/internal/validation"
	"go.uber.org/zap"
)

// checkOutcome은 한 검증 항목의 결과다(검증엔진이 돌려준 체크별 pass/detail).
type checkOutcome struct {
	Type   string
	Passed bool
	Detail string
}

// stepState는 세션 내 한 단계의 진행 상태다(프론트 StepProgress와 대응).
type stepState struct {
	StepID   int
	Status   string // pending | active | validating | passed | failed
	Attempts int
	Checks   []checkOutcome // 검증엔진 결과의 체크별 상세(실패 사유 표시용). 결과 수신 시 채워진다.
	// HintLevel: 이 스텝에서 지금까지 사용한 최고 힌트 레벨(0=미사용).
	// RequestHint 가 레벨 미지정 요청을 1→2→3 으로 자동 상승시키는 데 쓴다.
	HintLevel int
}

// sessionSteps는 한 세션의 스텝 진행 상태(전체 목록 + 현재 단계 id)를 묶어 보관한다.
type sessionSteps struct {
	LabID       string // 힌트/콘텐츠 조회용 — KubeVirt 조회 없이 lab DSL 에 접근하게 한다.
	UserID      string // 학습 이벤트(lab-events) 발행용 — 파티션 키.
	Steps       []stepState
	CurrentStep int
}

// allPassed는 모든 스텝이 passed 인지 반환한다(lab_completed 판정용).
func (ss *sessionSteps) allPassed() bool {
	if len(ss.Steps) == 0 {
		return false
	}
	for i := range ss.Steps {
		if ss.Steps[i].Status != "passed" {
			return false
		}
	}
	return true
}

// stepStore는 sessionID → 스텝 진행 상태를 관리한다.
// in-memory 맵이 캐시이고, db(persistence)가 설정되면 변경을 write-through 하고
// 캐시 미스를 DB에서 적재한다(progress.go) — API 재시작에도 진행 상태가 보존된다.
// db 가 nil 이면(로컬/CI) 종전과 동일한 in-memory 전용으로 동작한다.
type stepStore struct {
	mu  sync.Mutex
	m   map[string]*sessionSteps
	db  persistence
	log *zap.Logger
}

func newStepStore(db persistence, log *zap.Logger) *stepStore {
	return &stepStore{m: make(map[string]*sessionSteps), db: db, log: log}
}

// 유저별 세션 생성 직렬화는 handler.locks(lock.Locker)가 담당한다.
// 단일 세션 제약은 "활성 세션 조회 → 없으면 생성" 두 단계라 더블클릭 등 동시 요청이
// 둘 다 조회를 통과하는 TOCTOU 경합이 가능하다. Redis 락이면 다중 레플리카에서도
// 직렬화되고, 미설정 시 MemLocker 가 단일 인스턴스 내 직렬화를 제공한다.

// newSessionID는 짧은 hex 세션 id를 생성한다.
func newSessionID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newTraceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sessionCreationManager는 prepareSessionCreation이 필요로 하는 *kubevirt.Manager의 부분 집합이다.
// 전체 Manager 대신 이 인터페이스를 받게 해 실제 KubeVirt 클러스터 없이도 stub으로 단위 테스트한다.
type sessionCreationManager interface {
	FindActiveByUser(ctx context.Context, userID string) (string, error)
	Get(ctx context.Context, sessionID string) (*kubevirt.Session, error)
	Delete(ctx context.Context, sessionID string) error
}

// prepareSessionCreation은 기존 활성 세션을 확인하고, 이미 failed 상태인 세션은
// namespace를 정리해 사용자가 reaper 주기를 기다리지 않고 즉시 다시 시작할 수 있게 한다.
// cleanedSessionID는 handler가 남아 있는 진행 상태를 함께 제거하는 데 사용한다.
func prepareSessionCreation(ctx context.Context, sessions sessionCreationManager, userID string) (existingSessionID, cleanedSessionID string, err error) {
	existing, err := sessions.FindActiveByUser(ctx, userID)
	if err != nil || existing == "" {
		return existing, "", err
	}

	sess, err := sessions.Get(ctx, existing)
	if errors.Is(err, kubevirt.ErrNotFound) {
		return "", existing, nil
	}
	if err != nil {
		return "", "", err
	}
	if sess.Status != "failed" {
		return existing, "", nil
	}

	if err := sessions.Delete(ctx, existing); err != nil && !errors.Is(err, kubevirt.ErrNotFound) {
		return "", "", err
	}
	return "", existing, nil
}

// sessionResponse는 kubevirt.Session에 핸들러 레벨 보강 필드를 덧붙여 프론트 Session 계약에 맞춘다.
//   - current_step : 스텝 진행은 stepStore(in-memory STUB)에서 조회.
//   - terminal_url : lab.environment == "ubuntu" 일 때만(실시간 KubeVirt 터미널 제공 랩).
//   - vm_provider  : Phase-1 단일 프로바이더(kubevirt).
func (h *Handler) sessionResponse(s *kubevirt.Session) gin.H {
	out := gin.H{
		"id":          s.ID,
		"lab_id":      s.LabID,
		"user_id":     s.UserID,
		"status":      s.Status,
		"started_at":  s.StartedAt.UTC().Format(time.RFC3339),
		"expires_at":  s.ExpiresAt.UTC().Format(time.RFC3339),
		"vm_provider": "kubevirt",
	}
	out["current_step"] = 0
	h.steps.withSession(s.ID, func(ss *sessionSteps) bool {
		out["current_step"] = ss.CurrentStep
		return false
	})
	// 라이브 터미널 랩이 실제 사용 가능한 상태일 때만 WS 경로 제공.
	if lc, ok := h.labs[s.LabID]; ok && lc.HasLiveTerminal() && (s.Status == "ready" || s.Status == "active") {
		out["terminal_url"] = "/api/v1/sessions/" + s.ID + "/ws"
		// IDE 랩(code-server)은 브라우저 VS Code 프록시 경로도 함께 제공.
		if lc.IDE {
			out["ide_url"] = "/api/v1/sessions/" + s.ID + "/ide/"
		}
	}
	return out
}

// CreateSession은 lab 콘텐츠를 확인한 뒤 KubeVirt VM 세션을 생성하고 스텝 진행 상태를 초기화한다.
// POST /api/v1/sessions  body: { "lab_id": "lab-k8s-basics" }
func (h *Handler) CreateSession(c *gin.Context) {
	if h.sessions == nil {
		h.err(c, http.StatusServiceUnavailable, "kubevirt not configured")
		return
	}
	var req struct {
		LabID string `json:"lab_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.err(c, http.StatusBadRequest, err.Error())
		return
	}
	// 콘텐츠가 없는 lab은 세션을 시작할 수 없다(스텝/검증 흐름 진행 불가).
	lc, ok := h.labs[req.LabID]
	if !ok {
		h.err(c, http.StatusNotFound, "lab content not found")
		return
	}
	userID, _ := c.Get("user_id")
	uid, _ := userID.(string)

	// 한 유저당 활성 세션 1개로 제한한다. uid가 있으면 유저별 락으로 동시요청을 직렬화하고
	// (락은 생성 완료까지 유지), 이미 활성 세션이 있으면 409로 거부한다.
	if uid != "" {
		release, ok := h.locks.Acquire(c.Request.Context(), "session-create:"+uid)
		if !ok {
			// 분산 락 경합(다른 생성 요청 진행 중) 또는 Redis 오류 — 잠시 후 재시도 유도.
			c.JSON(http.StatusConflict, gin.H{
				"error": "session operation in progress, try again",
				"code":  "session_locked",
			})
			return
		}
		defer release()

		existing, cleaned, err := prepareSessionCreation(c.Request.Context(), h.sessions, uid)
		if err != nil {
			h.log.Error("prepare session creation", zap.Error(err))
			h.err(c, http.StatusInternalServerError, "check active session failed")
			return
		}
		if cleaned != "" {
			h.steps.remove(cleaned)
		}
		if existing != "" {
			c.JSON(http.StatusConflict, gin.H{
				"error":      "active session already exists",
				"code":       "session_exists",
				"session_id": existing,
			})
			return
		}
	}

	// 동시 활성 세션 쿼터 — 용량 초과 무한 생성으로 스토리지가 마르는 것을 막는다(0이면 무제한).
	if max := h.cfg.KubeVirt.MaxActiveSessions; max > 0 {
		active, err := h.sessions.CountActiveSessions(c.Request.Context())
		if err != nil {
			h.log.Error("count active sessions", zap.Error(err))
			h.err(c, http.StatusInternalServerError, "check session capacity failed")
			return
		}
		if active >= max {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "session capacity reached, try again later",
				"code":  "capacity_reached",
			})
			return
		}
	}

	// 랩별 초기화(init)는 cloud-init 으로 VM 부팅 시 실행된다(도구 설치 등).
	sess, err := h.sessions.Create(c.Request.Context(), newSessionID(), req.LabID, uid, kubevirt.BootInit{
		Packages: lc.Init.Packages,
		Runcmd:   lc.Init.Runcmd,
	})
	if err != nil {
		h.log.Error("create session", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "create session failed")
		return
	}

	// 스텝 진행 상태 초기화 — 첫 스텝 active, 나머지 pending.
	ss := &sessionSteps{LabID: req.LabID, UserID: uid}
	for i, st := range lc.Steps {
		status := "pending"
		if i == 0 {
			status = "active"
		}
		ss.Steps = append(ss.Steps, stepState{StepID: st.ID, Status: status})
	}
	if len(ss.Steps) > 0 {
		ss.CurrentStep = ss.Steps[0].StepID
	}
	h.steps.put(sess.ID, ss)

	// 학습 분석: vm_provisioned_source 로 온프렘/EC2 분포를 집계한다(Phase-1: kubevirt 고정).
	h.emitEvent(events.Event{
		Type: events.LabStarted, UserID: uid, SessionID: sess.ID, LabID: req.LabID,
		VMProvider: "kubevirt",
	})

	c.JSON(http.StatusCreated, h.sessionResponse(sess))
}

// GetSession은 세션 상태를 반환한다(current_step·terminal_url은 sessionResponse에서 보강).
// GET /api/v1/sessions/:id
func (h *Handler) GetSession(c *gin.Context) {
	if h.sessions == nil {
		h.err(c, http.StatusServiceUnavailable, "kubevirt not configured")
		return
	}
	sess, err := h.sessions.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, kubevirt.ErrNotFound) {
			h.err(c, http.StatusNotFound, "session not found")
			return
		}
		h.log.Error("get session", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "get session failed")
		return
	}
	if h.denyIfNotSessionOwner(c, sess) {
		return
	}
	c.JSON(http.StatusOK, h.sessionResponse(sess))
}

// DeleteSession은 세션 네임스페이스를 삭제한다(VM cascade 삭제) + 스텝 상태도 정리한다.
// DELETE /api/v1/sessions/:id
func (h *Handler) DeleteSession(c *gin.Context) {
	if h.sessions == nil {
		h.err(c, http.StatusServiceUnavailable, "kubevirt not configured")
		return
	}
	id := c.Param("id")

	// 삭제 전 소유자 확인 — 세션 ID 추측만으로 타인의 실습을 종료시킬 수 없게 한다.
	sess, err := h.sessions.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, kubevirt.ErrNotFound) {
			h.err(c, http.StatusNotFound, "session not found")
			return
		}
		h.log.Error("get session for delete", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "delete session failed")
		return
	}
	if h.denyIfNotSessionOwner(c, sess) {
		return
	}

	if err := h.sessions.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, kubevirt.ErrNotFound) {
			h.err(c, http.StatusNotFound, "session not found")
			return
		}
		h.log.Error("delete session", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "delete session failed")
		return
	}
	ss, tracked := h.steps.take(id)

	// 전 스텝 통과 전 종료는 이탈(lab_abandoned)로 기록한다 — 막힘 분포 분석의 입력.
	// lab_completed 는 마지막 스텝 통과 시점(ApplyValidationResult)에 이미 발행됐다.
	if tracked && !ss.allPassed() {
		h.emitEvent(events.Event{
			Type: events.LabAbandoned, UserID: ss.UserID, SessionID: id, LabID: ss.LabID,
		})
	}
	c.Status(http.StatusNoContent)
}

// ReapStuckSessions는 프로비저닝 타임아웃을 넘겨 ready가 되지 못한 세션을 회수하고 stepStore도 정리한다.
// main의 백그라운드 루프가 주기적으로 호출한다. sessions 미설정 또는 timeout<=0 이면 no-op.
func (h *Handler) ReapStuckSessions(ctx context.Context) {
	if h.sessions == nil {
		return
	}
	timeout := time.Duration(h.cfg.KubeVirt.ProvisionTimeoutMinutes) * time.Minute
	if timeout <= 0 {
		return
	}
	reaped, err := h.sessions.ReapStuckSessions(ctx, timeout)
	if err != nil {
		h.log.Error("reap stuck sessions", zap.Error(err))
		return
	}
	if len(reaped) == 0 {
		return
	}
	for _, id := range reaped {
		h.steps.remove(id)
	}
	h.log.Warn("프로비저닝 타임아웃 세션 회수",
		zap.Strings("session_ids", reaped),
		zap.Duration("timeout", timeout),
	)
}

// ReapExpiredSessions는 TTL(expires_at)이 지난 세션을 회수하고 stepStore도 정리한다.
// 미완료 세션 만료는 lab_abandoned 로 기록한다(타임아웃 분포 분석의 입력 — 막혀서 시간을
// 다 쓴 케이스). main의 백그라운드 루프가 주기적으로 호출한다. sessions 미설정 시 no-op.
func (h *Handler) ReapExpiredSessions(ctx context.Context) {
	if h.sessions == nil {
		return
	}
	reaped, err := h.sessions.ReapExpiredSessions(ctx)
	if err != nil {
		h.log.Error("reap expired sessions", zap.Error(err))
		return
	}
	if len(reaped) == 0 {
		return
	}
	for _, id := range reaped {
		// take 로 마지막 상태를 받아 미완료면 이탈 이벤트를 남긴다(DeleteSession 과 동일 의미).
		ss, tracked := h.steps.take(id)
		if tracked && !ss.allPassed() {
			h.emitEvent(events.Event{
				Type: events.LabAbandoned, UserID: ss.UserID, SessionID: id, LabID: ss.LabID,
			})
		}
	}
	h.log.Warn("TTL 만료 세션 회수", zap.Strings("session_ids", reaped))
}

// GetSessionSteps는 세션의 단계별 진행 상태 목록을 반환한다(프론트 StepProgress[]).
// GET /api/v1/sessions/:id/steps
func (h *Handler) GetSessionSteps(c *gin.Context) {
	if h.denyIfNotStoreOwner(c, c.Param("id")) {
		return
	}
	var items []gin.H
	found := h.steps.withSession(c.Param("id"), func(ss *sessionSteps) bool {
		items = make([]gin.H, 0, len(ss.Steps))
		for _, st := range ss.Steps {
			item := gin.H{"step_id": st.StepID, "status": st.Status, "attempts": st.Attempts}
			// 검증엔진 결과가 있으면 체크별 상세(실패 사유)를 함께 노출한다.
			if len(st.Checks) > 0 {
				checks := make([]gin.H, 0, len(st.Checks))
				for _, ck := range st.Checks {
					checks = append(checks, gin.H{"type": ck.Type, "passed": ck.Passed, "detail": ck.Detail})
				}
				item["checks"] = checks
			}
			items = append(items, item)
		}
		return false
	})
	if !found {
		h.err(c, http.StatusNotFound, "session not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// ValidateStep은 한 단계의 검증 요청을 validation-engine으로 발행하고 스텝을 validating으로 둔다.
// 실제 pass/fail은 ApplyValidationResult가 validation-results 결과를 받아 확정한다(프론트는 폴링).
// Kafka publisher가 없으면(로컬/CI) 종전대로 mock 통과한다.
// POST /api/v1/sessions/:id/validate  body: { "step_id": 1 }
func (h *Handler) ValidateStep(c *gin.Context) {
	var req struct {
		StepID int `json:"step_id" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.err(c, http.StatusBadRequest, "step_id is required")
		return
	}

	sessionID := c.Param("id")
	if h.denyIfNotStoreOwner(c, sessionID) {
		return
	}
	idx, err := h.findStepIndex(sessionID, req.StepID)
	if err != nil {
		if errors.Is(err, errStepSessionNotFound) {
			h.err(c, http.StatusNotFound, "session not found")
		} else {
			h.err(c, http.StatusNotFound, "step not found")
		}
		return
	}

	if h.validator == nil {
		h.markStepPassed(sessionID, idx)
		c.JSON(http.StatusOK, gin.H{"status": "passed", "message": "검증을 통과했습니다 (mock)"})
		return
	}
	if h.sessions == nil {
		h.err(c, http.StatusServiceUnavailable, "kubevirt not configured")
		return
	}

	sess, err := h.sessions.Get(c.Request.Context(), sessionID)
	if err != nil {
		if errors.Is(err, kubevirt.ErrNotFound) {
			h.err(c, http.StatusNotFound, "session not found")
			return
		}
		h.log.Error("get session for validation", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "get session failed")
		return
	}

	lc, ok := h.labs[sess.LabID]
	if !ok {
		h.err(c, http.StatusNotFound, "lab content not found")
		return
	}
	step, ok := findContentStep(lc, req.StepID)
	if !ok {
		h.err(c, http.StatusNotFound, "step content not found")
		return
	}
	if len(step.Checks) == 0 {
		h.err(c, http.StatusBadRequest, "step has no validation checks")
		return
	}

	traceID := newTraceID()
	msg := validation.ValidationRequest{
		TraceID:   traceID,
		SessionID: sessionID,
		StepID:    req.StepID,
		VM: validation.VMSpec{
			Type:      validation.VMTypeKubeVirt,
			Name:      "session-vm",
			Namespace: "lab-" + sessionID,
		},
		Checks: toValidationChecks(step.Checks),
	}
	if err := h.validator.PublishRequest(c.Request.Context(), msg); err != nil {
		h.log.Error("publish validation request", zap.Error(err), zap.String("session_id", sessionID), zap.Int("step_id", req.StepID))
		h.err(c, http.StatusBadGateway, "publish validation request failed")
		return
	}

	// 결과가 올 때까지 validating으로 둔다. 실제 pass/fail은 ApplyValidationResult가 확정한다.
	h.markStepValidating(sessionID, idx)
	c.JSON(http.StatusAccepted, gin.H{
		"status":   "validating",
		"message":  "검증 요청을 접수했습니다",
		"trace_id": traceID,
	})
}

// markStepValidating은 검증 요청 발행 후 스텝을 validating으로 두고 시도 횟수를 올린다.
// 재시도 시 이전 체크 상세는 비운다.
func (h *Handler) markStepValidating(sessionID string, idx int) {
	h.steps.withSession(sessionID, func(ss *sessionSteps) bool {
		if idx < 0 || idx >= len(ss.Steps) {
			return false
		}
		ss.Steps[idx].Attempts++
		ss.Steps[idx].Status = "validating"
		ss.Steps[idx].Checks = nil
		return true
	})
}

// ApplyValidationResult는 검증엔진 결과(validation-results)를 stepStore에 반영한다.
// consumer goroutine에서 호출되며, (session_id, step_id)로 스텝을 찾아 체크별 상세를 저장하고
// 모두 통과면 passed로 확정 후 다음 스텝을 활성화한다. 실패면 failed로 두되 스텝을 진행시키지
// 않아(current 유지) 사용자가 다시 시도할 수 있게 한다. 모르는 세션/스텝은 무시한다(지연 결과 등).
func (h *Handler) ApplyValidationResult(r validation.ValidationResult) {
	var completedUser, completedLab string
	found := h.steps.withSession(r.SessionID, func(ss *sessionSteps) bool {
		idx := -1
		for i := range ss.Steps {
			if ss.Steps[i].StepID == r.StepID {
				idx = i
				break
			}
		}
		if idx == -1 {
			h.log.Warn("검증 결과의 스텝을 찾을 수 없음", zap.String("session_id", r.SessionID), zap.Int("step_id", r.StepID))
			return false
		}

		outcomes := make([]checkOutcome, 0, len(r.Checks))
		for _, ck := range r.Checks {
			outcomes = append(outcomes, checkOutcome{Type: string(ck.Type), Passed: ck.Passed, Detail: ck.Detail})
		}
		ss.Steps[idx].Checks = outcomes
		if r.Passed {
			ss.Steps[idx].Status = "passed"
			if idx+1 < len(ss.Steps) {
				if ss.Steps[idx+1].Status == "pending" {
					ss.Steps[idx+1].Status = "active"
				}
				ss.CurrentStep = ss.Steps[idx+1].StepID
			}
			h.emitEvent(events.Event{
				Type: events.StepCompleted, UserID: ss.UserID, SessionID: r.SessionID,
				LabID: ss.LabID, StepID: r.StepID,
			})
			// 마지막 스텝까지 모두 통과한 시점이 랩 완료다(세션 삭제와 무관하게 1회 발행).
			if ss.allPassed() {
				h.emitEvent(events.Event{
					Type: events.LabCompleted, UserID: ss.UserID, SessionID: r.SessionID, LabID: ss.LabID,
				})
				completedUser, completedLab = ss.UserID, ss.LabID
			}
		} else {
			ss.Steps[idx].Status = "failed"
			h.emitEvent(events.Event{
				Type: events.ValidationFailed, UserID: ss.UserID, SessionID: r.SessionID,
				LabID: ss.LabID, StepID: r.StepID,
			})
		}
		return true
	})
	if !found {
		h.log.Warn("검증 결과의 세션을 찾을 수 없음", zap.String("session_id", r.SessionID), zap.Int("step_id", r.StepID))
		return
	}

	// 랩 완료 이력(수료증·배지·리더보드의 원천). (user, lab) 최초 1회만 기록된다.
	if completedLab != "" && completedUser != "" && h.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
		defer cancel()
		if err := h.db.RecordCompletion(ctx, completedUser, completedLab, r.SessionID); err != nil {
			h.log.Warn("랩 완료 이력 기록 실패", zap.String("session_id", r.SessionID), zap.Error(err))
		}
	}
}

var errStepSessionNotFound = errors.New("session not found")
var errStepNotFound = errors.New("step not found")

func (h *Handler) findStepIndex(sessionID string, stepID int) (int, error) {
	idx := -1
	found := h.steps.withSession(sessionID, func(ss *sessionSteps) bool {
		for i := range ss.Steps {
			if ss.Steps[i].StepID == stepID {
				idx = i
				break
			}
		}
		return false
	})
	if !found {
		return -1, errStepSessionNotFound
	}
	if idx == -1 {
		return -1, errStepNotFound
	}
	return idx, nil
}

func (h *Handler) markStepPassed(sessionID string, idx int) {
	h.steps.withSession(sessionID, func(ss *sessionSteps) bool {
		if idx < 0 || idx >= len(ss.Steps) {
			return false
		}
		ss.Steps[idx].Attempts++
		ss.Steps[idx].Status = "passed"
		if idx+1 < len(ss.Steps) {
			if ss.Steps[idx+1].Status == "pending" {
				ss.Steps[idx+1].Status = "active"
			}
			ss.CurrentStep = ss.Steps[idx+1].StepID
		}
		return true
	})
}

func findContentStep(lc content.LabContent, stepID int) (content.Step, bool) {
	for _, step := range lc.Steps {
		if step.ID == stepID {
			return step, true
		}
	}
	return content.Step{}, false
}

func toValidationChecks(checks []content.Check) []validation.Check {
	out := make([]validation.Check, 0, len(checks))
	for _, check := range checks {
		out = append(out, validation.Check{
			Type:       validation.CheckType(check.Type),
			Command:    check.Command,
			Path:       check.Path,
			URL:        check.URL,
			Name:       check.Name,
			Expect:     check.Expect,
			ExpectCode: check.ExpectCode,
		})
	}
	return out
}
