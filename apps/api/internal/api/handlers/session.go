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
// LabID는 검증 요청 발행 시 해당 스텝의 checks를 lab 콘텐츠에서 찾기 위해 보관한다.
type sessionSteps struct {
	LabID       string
	Steps       []stepState
	CurrentStep int
}

// stepStore는 sessionID → 스텝 진행 상태 맵을 동시성 안전하게 관리한다.
// ValidateStep은 검증 요청을 Kafka로 발행하지만, 결과(validation-results) 소비는 후속 작업이라
// 현재 상태 전이는 mock 통과로 유지된다. 결과 토픽 구독을 붙일 때 본 스토어를 결과 기반으로 교체한다.
type stepStore struct {
	mu sync.Mutex
	m  map[string]*sessionSteps
}

func newStepStore() *stepStore { return &stepStore{m: make(map[string]*sessionSteps)} }

// newSessionID는 짧은 hex 세션 id를 생성한다.
func newSessionID() string {
	b := make([]byte, 4)
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

	sess, err := h.sessions.Create(c.Request.Context(), newSessionID(), req.LabID, uid)
	if err != nil {
		h.log.Error("create session", zap.Error(err))
		h.err(c, http.StatusInternalServerError, "create session failed")
		return
	}

	// 스텝 진행 상태 초기화 — 첫 스텝 active, 나머지 pending.
	ss := &sessionSteps{LabID: req.LabID}
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

// ValidateStep은 한 단계의 검증을 수행한다.
// POST /api/v1/sessions/:id/validate  body: { "step_id": 1 }
//
// 검증엔진 연동(dark launch): 해당 스텝의 checks로 검증 요청을 Kafka(validation-requests)에
// 발행한다. 단, 검증 결과(validation-results) 소비는 후속 작업이라, 지금은 결과를 기다리지
// 않고 종전과 같이 mock 통과로 상태를 전이한다. Kafka 미설정(h.dispatch == nil) 시 발행을
// 생략하므로 동작에 영향이 없다.
func (h *Handler) ValidateStep(c *gin.Context) {
	var req struct {
		StepID int `json:"step_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.err(c, http.StatusBadRequest, "step_id is required")
		return
	}
	sessionID := c.Param("id")

	h.steps.mu.Lock()
	ss, ok := h.steps.m[sessionID]
	if !ok {
		h.steps.mu.Unlock()
		h.err(c, http.StatusNotFound, "session not found")
		return
	}
	idx := -1
	for i := range ss.Steps {
		if ss.Steps[i].StepID == req.StepID {
			idx = i
			break
		}
	}
	if idx == -1 {
		h.steps.mu.Unlock()
		h.err(c, http.StatusNotFound, "step not found")
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
	labID := ss.LabID
	h.steps.mu.Unlock()

	// 락 밖에서 발행(네트워크 I/O가 다른 스텝 연산을 막지 않도록).
	h.dispatchValidation(c.Request.Context(), sessionID, labID, req.StepID)

	c.JSON(http.StatusOK, gin.H{"status": "passed", "message": "검증을 통과했습니다 (mock)"})
}

// dispatchValidation은 lab 콘텐츠에서 해당 스텝의 checks를 찾아 검증 요청을 발행한다.
// 발행 실패는 로깅만 하고 요청 처리에는 영향을 주지 않는다(best-effort).
func (h *Handler) dispatchValidation(ctx context.Context, sessionID, labID string, stepID int) {
	if h.dispatch == nil {
		return
	}
	lc, ok := h.labs[labID]
	if !ok {
		return
	}
	var checks []content.Check
	for _, st := range lc.Steps {
		if st.ID == stepID {
			checks = st.Checks
			break
		}
	}
	if len(checks) == 0 {
		return // 검증엔진은 빈 checks를 요청 오류로 거절하므로 발행하지 않는다.
	}

	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := h.dispatch.Publish(pubCtx, validation.Request{
		SessionID: sessionID,
		StepID:    stepID,
		VM: validation.VMSpec{
			Type:      validation.VMTypeKubeVirt,
			Name:      "session-vm",       // kubevirt.Manager.Create가 고정 생성하는 VM 이름
			Namespace: "lab-" + sessionID, // 세션 네임스페이스 규칙
		},
		Checks: checks,
	})
	if err != nil {
		h.log.Error("검증 요청 발행 실패",
			zap.String("session_id", sessionID),
			zap.Int("step_id", stepID),
			zap.Error(err),
		)
	}
}
