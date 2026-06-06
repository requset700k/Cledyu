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
}

// sessionSteps는 한 세션의 스텝 진행 상태(전체 목록 + 현재 단계 id)를 묶어 보관한다.
// LabID는 검증 요청 발행 시 해당 스텝의 checks를 lab 콘텐츠에서 찾기 위해 보관한다.
type sessionSteps struct {
	LabID       string
	Steps       []stepState
	CurrentStep int
}

// stepStore는 sessionID → 스텝 진행 상태 맵을 동시성 안전하게 관리한다.
// ValidateStep이 검증 요청을 발행하면 스텝은 validating 상태가 되고, 검증엔진 결과를
// 소비하는 ApplyValidationResult가 passed/failed로 확정한다(Kafka 미설정 시에는 mock 통과 폴백).
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
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// ValidateStep은 한 단계의 검증을 수행한다.
// POST /api/v1/sessions/:id/validate  body: { "step_id": 1 }
//
// 검증엔진 연동 시(h.dispatch != nil 이고 스텝에 checks 존재): 해당 스텝을 validating으로 두고
// 검증 요청을 Kafka(validation-requests)에 발행한 뒤 202(validating)로 즉시 응답한다. 실제
// pass/fail은 검증엔진이 validation-results로 돌려준 결과를 ApplyValidationResult가 확정하며,
// 프론트는 GET /steps 폴링으로 상태 변화를 관찰한다.
// Kafka 미설정 또는 스텝에 checks가 없으면: 검증할 대상이 없으므로 종전대로 mock 통과한다.
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
	labID := ss.LabID
	checks := stepChecks(h.labs, labID, req.StepID)
	real := h.dispatch != nil && len(checks) > 0

	ss.Steps[idx].Attempts++
	if real {
		// 결과가 올 때까지 validating. 재시도 시 이전 체크 상세는 비운다.
		ss.Steps[idx].Status = "validating"
		ss.Steps[idx].Checks = nil
	} else {
		ss.Steps[idx].Status = "passed"
		advanceStep(ss, idx)
	}
	h.steps.mu.Unlock()

	if !real {
		c.JSON(http.StatusOK, gin.H{"status": "passed", "message": "검증을 통과했습니다 (mock)"})
		return
	}

	// 락 밖에서 발행(네트워크 I/O가 다른 스텝 연산을 막지 않도록).
	h.publishValidation(c.Request.Context(), sessionID, req.StepID, checks)
	c.JSON(http.StatusAccepted, gin.H{"status": "validating", "message": "검증 요청을 보냈습니다"})
}

// ApplyValidationResult는 검증엔진 결과(validation-results)를 stepStore에 반영한다.
// consumer goroutine에서 호출되며, (session_id, step_id)로 해당 스텝을 찾아 체크 상세를 저장하고
// 모두 통과면 passed로 확정 후 다음 스텝을 활성화한다. 실패면 failed로 두되 스텝을 진행시키지
// 않아(current 유지) 사용자가 다시 시도할 수 있게 한다. 모르는 세션/스텝은 무시한다(지연 결과 등).
func (h *Handler) ApplyValidationResult(r validation.Result) {
	h.steps.mu.Lock()
	defer h.steps.mu.Unlock()
	ss, ok := h.steps.m[r.SessionID]
	if !ok {
		h.log.Warn("검증 결과의 세션을 찾을 수 없음", zap.String("session_id", r.SessionID), zap.Int("step_id", r.StepID))
		return
	}
	idx := -1
	for i := range ss.Steps {
		if ss.Steps[i].StepID == r.StepID {
			idx = i
			break
		}
	}
	if idx == -1 {
		h.log.Warn("검증 결과의 스텝을 찾을 수 없음", zap.String("session_id", r.SessionID), zap.Int("step_id", r.StepID))
		return
	}

	outcomes := make([]checkOutcome, 0, len(r.Checks))
	for _, c := range r.Checks {
		outcomes = append(outcomes, checkOutcome{Type: c.Type, Passed: c.Passed, Detail: c.Detail})
	}
	ss.Steps[idx].Checks = outcomes
	if r.Passed {
		ss.Steps[idx].Status = "passed"
		advanceStep(ss, idx)
	} else {
		ss.Steps[idx].Status = "failed"
	}
}

// advanceStep은 idx 스텝 통과 후 다음 스텝을 active로 올리고 현재 스텝 포인터를 옮긴다.
// 호출자는 stepStore 락을 보유해야 한다.
func advanceStep(ss *sessionSteps, idx int) {
	if idx+1 < len(ss.Steps) {
		if ss.Steps[idx+1].Status == "pending" {
			ss.Steps[idx+1].Status = "active"
		}
		ss.CurrentStep = ss.Steps[idx+1].StepID
	}
}

// stepChecks는 lab 콘텐츠에서 해당 스텝의 검증 항목을 찾는다(없으면 nil).
func stepChecks(labs map[string]content.LabContent, labID string, stepID int) []content.Check {
	lc, ok := labs[labID]
	if !ok {
		return nil
	}
	for _, st := range lc.Steps {
		if st.ID == stepID {
			return st.Checks
		}
	}
	return nil
}

// publishValidation은 검증 요청을 발행한다. 발행 실패는 로깅만 하고 요청 처리에는 영향을 주지 않는다.
// 단 발행이 실패하면 결과가 영영 오지 않아 validating에 머무르므로, 스텝을 failed로 되돌려 재시도를 유도한다.
func (h *Handler) publishValidation(ctx context.Context, sessionID string, stepID int, checks []content.Check) {
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
		// 발행 실패 → 결과 미수신 → validating 고착 방지: failed로 되돌린다.
		h.steps.mu.Lock()
		if ss, ok := h.steps.m[sessionID]; ok {
			for i := range ss.Steps {
				if ss.Steps[i].StepID == stepID && ss.Steps[i].Status == "validating" {
					ss.Steps[i].Status = "failed"
					break
				}
			}
		}
		h.steps.mu.Unlock()
	}
}
