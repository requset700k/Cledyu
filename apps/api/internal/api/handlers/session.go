package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/content"
	"github.com/requset700k/cledyu/api/internal/kubevirt"
	"github.com/requset700k/cledyu/api/internal/validation"
	"go.uber.org/zap"
)

// stepState는 세션 내 한 단계의 진행 상태다(프론트 StepProgress와 대응).
type stepState struct {
	StepID   int
	Status   string // pending | active | passed | failed
	Attempts int
}

// sessionSteps는 한 세션의 스텝 진행 상태(전체 목록 + 현재 단계 id)를 묶어 보관한다.
type sessionSteps struct {
	Steps       []stepState
	CurrentStep int
}

// stepStore는 sessionID → 스텝 진행 상태 맵을 동시성 안전하게 관리한다.
// publisher 미연결 시 mock 통과로 동작. 실 검증엔진 연동 시 본 스토어를 제거하고
// 검증 결과 토픽을 구독하도록 교체할 예정.
type stepStore struct {
	mu sync.Mutex
	m  map[string]*sessionSteps
}

func newStepStore() *stepStore { return &stepStore{m: make(map[string]*sessionSteps)} }

// userLocks는 유저별 mutex를 보관해 같은 유저의 동시 세션 생성을 직렬화한다.
// 단일 세션 제약은 "활성 세션 조회 → 없으면 생성" 두 단계라, 더블클릭 등 동시 요청이
// 둘 다 조회를 통과하는 TOCTOU 경합이 가능하다. 유저별 락으로 단일 API 인스턴스 내에서는 이를 막는다
// (다중 레플리카에서는 best-effort — 완전 차단엔 별도 분산 락이 필요).
type userLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func newUserLocks() *userLocks { return &userLocks{m: make(map[string]*sync.Mutex)} }

// get은 userID 전용 mutex를 반환한다(없으면 생성).
func (u *userLocks) get(userID string) *sync.Mutex {
	u.mu.Lock()
	defer u.mu.Unlock()
	lk, ok := u.m[userID]
	if !ok {
		lk = &sync.Mutex{}
		u.m[userID] = lk
	}
	return lk
}

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
	h.steps.mu.Lock()
	if ss, ok := h.steps.m[s.ID]; ok {
		out["current_step"] = ss.CurrentStep
	} else {
		out["current_step"] = 0
	}
	h.steps.mu.Unlock()
	// 라이브 터미널 랩만 WS 경로 제공.
	if lc, ok := h.labs[s.LabID]; ok && lc.HasLiveTerminal() {
		out["terminal_url"] = "/api/v1/sessions/" + s.ID + "/ws"
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
		lk := h.userLocks.get(uid)
		lk.Lock()
		defer lk.Unlock()

		existing, err := h.sessions.FindActiveByUser(c.Request.Context(), uid)
		if err != nil {
			h.log.Error("find active session", zap.Error(err))
			h.err(c, http.StatusInternalServerError, "check active session failed")
			return
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

	sess, err := h.sessions.Create(c.Request.Context(), newSessionID(), req.LabID, uid)
	if err != nil {
		h.log.Error("create session", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "create session failed")
		return
	}

	// 스텝 진행 상태 초기화 — 첫 스텝 active, 나머지 pending.
	ss := &sessionSteps{}
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
	h.steps.mu.Lock()
	h.steps.m[sess.ID] = ss
	h.steps.mu.Unlock()

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
	if err := h.sessions.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, kubevirt.ErrNotFound) {
			h.err(c, http.StatusNotFound, "session not found")
			return
		}
		h.log.Error("delete session", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "delete session failed")
		return
	}
	h.steps.mu.Lock()
	delete(h.steps.m, id)
	h.steps.mu.Unlock()
	c.Status(http.StatusNoContent)
}

// GetSessionSteps는 세션의 단계별 진행 상태 목록을 반환한다(프론트 StepProgress[]).
// GET /api/v1/sessions/:id/steps
func (h *Handler) GetSessionSteps(c *gin.Context) {
	h.steps.mu.Lock()
	defer h.steps.mu.Unlock()
	ss, ok := h.steps.m[c.Param("id")]
	if !ok {
		h.err(c, http.StatusNotFound, "session not found")
		return
	}
	items := make([]gin.H, 0, len(ss.Steps))
	for _, st := range ss.Steps {
		items = append(items, gin.H{"step_id": st.StepID, "status": st.Status, "attempts": st.Attempts})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// ValidateStep은 한 단계의 검증 요청을 validation-engine으로 발행한다.
// Kafka publisher가 없으면 기존 mock 통과 동작을 유지한다(실제 publisher 연결은 후속 PR).
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

	h.recordStepAttempt(sessionID, idx)
	c.JSON(http.StatusAccepted, gin.H{
		"status":   "accepted",
		"message":  "검증 요청을 접수했습니다",
		"trace_id": traceID,
	})
}

var errStepSessionNotFound = errors.New("session not found")
var errStepNotFound = errors.New("step not found")

func (h *Handler) findStepIndex(sessionID string, stepID int) (int, error) {
	h.steps.mu.Lock()
	defer h.steps.mu.Unlock()
	ss, ok := h.steps.m[sessionID]
	if !ok {
		return -1, errStepSessionNotFound
	}
	for i := range ss.Steps {
		if ss.Steps[i].StepID == stepID {
			return i, nil
		}
	}
	return -1, errStepNotFound
}

func (h *Handler) recordStepAttempt(sessionID string, idx int) {
	h.steps.mu.Lock()
	defer h.steps.mu.Unlock()
	ss, ok := h.steps.m[sessionID]
	if !ok || idx < 0 || idx >= len(ss.Steps) {
		return
	}
	ss.Steps[idx].Attempts++
}

func (h *Handler) markStepPassed(sessionID string, idx int) {
	h.steps.mu.Lock()
	defer h.steps.mu.Unlock()
	ss, ok := h.steps.m[sessionID]
	if !ok || idx < 0 || idx >= len(ss.Steps) {
		return
	}
	ss.Steps[idx].Attempts++
	ss.Steps[idx].Status = "passed"
	if idx+1 < len(ss.Steps) {
		if ss.Steps[idx+1].Status == "pending" {
			ss.Steps[idx+1].Status = "active"
		}
		ss.CurrentStep = ss.Steps[idx+1].StepID
	}
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
