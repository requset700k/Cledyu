package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// STUB: 아래 세션 핸들러는 실제 Session API / VM 오케스트레이터(별도 도메인) 구현 전까지의
// 임시 in-memory mock이다. VM 프로비저닝·xterm 터미널·Kafka 검증엔진 연동이 없으며,
// 서버 재기동 시 상태가 초기화된다. 프론트엔드가 실제 api.sessions.* 계약을 검증/시연할 수 있게 하는 것이 목적이다.

// stepState는 세션 내 한 단계의 진행 상태다(프론트 StepProgress와 대응).
type stepState struct {
	StepID   int
	Status   string // pending | active | passed | failed
	Attempts int
}

// mockSession은 메모리에 보관되는 mock 랩 세션이다.
type mockSession struct {
	ID          string
	LabID       string
	UserID      string
	Status      string // provisioning | ready | active | completed | failed
	CurrentStep int
	StartedAt   time.Time
	ExpiresAt   time.Time
	Steps       []stepState
}

// sessionStore는 mock 세션을 동시성 안전하게 보관한다.
type sessionStore struct {
	mu sync.Mutex
	m  map[string]*mockSession
}

func newSessionStore() *sessionStore {
	return &sessionStore{m: make(map[string]*mockSession)}
}

// newSessionID는 충돌 가능성이 낮은 mock 세션 id를 생성한다.
func newSessionID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "sess-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "sess-" + hex.EncodeToString(b)
}

func sessionJSON(s *mockSession) gin.H {
	return gin.H{
		"id":           s.ID,
		"lab_id":       s.LabID,
		"user_id":      s.UserID,
		"status":       s.Status,
		"vm_provider":  "kubevirt",
		"current_step": s.CurrentStep,
		"started_at":   s.StartedAt.UTC().Format(time.RFC3339),
		"expires_at":   s.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// CreateSession은 mock 랩 세션을 생성한다. STUB — 실제 VM 프로비저닝 없이 즉시 ready 상태가 된다.
// POST /api/v1/sessions  body: { "lab_id": "lab-k8s-basics" }
func (h *Handler) CreateSession(c *gin.Context) {
	var req struct {
		LabID string `json:"lab_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.LabID == "" {
		h.err(c, http.StatusBadRequest, "lab_id is required")
		return
	}

	lc, ok := h.labs[req.LabID]
	if !ok {
		// 스텝 콘텐츠가 없는 랩은 mock 세션을 시작할 수 없다.
		h.err(c, http.StatusNotFound, "lab content not found")
		return
	}

	userID, _ := c.Get("user_id")
	uid, _ := userID.(string)

	now := time.Now()
	sess := &mockSession{
		ID:          newSessionID(),
		LabID:       req.LabID,
		UserID:      uid,
		Status:      "ready", // mock: 프로비저닝 즉시 완료
		CurrentStep: 1,
		StartedAt:   now,
		ExpiresAt:   now.Add(3 * time.Hour), // 세션 최대 유지 3시간
	}
	for i, st := range lc.Steps {
		status := "pending"
		if i == 0 {
			status = "active"
		}
		sess.Steps = append(sess.Steps, stepState{StepID: st.ID, Status: status})
	}
	if len(sess.Steps) > 0 {
		sess.CurrentStep = sess.Steps[0].StepID
	}

	h.sessions.mu.Lock()
	h.sessions.m[sess.ID] = sess
	body := sessionJSON(sess)
	h.sessions.mu.Unlock()

	c.JSON(http.StatusCreated, body)
}

// GetSession은 세션 상태를 반환한다.
// GET /api/v1/sessions/:id
func (h *Handler) GetSession(c *gin.Context) {
	h.sessions.mu.Lock()
	defer h.sessions.mu.Unlock()

	s, ok := h.sessions.m[c.Param("id")]
	if !ok {
		h.err(c, http.StatusNotFound, "session not found")
		return
	}
	c.JSON(http.StatusOK, sessionJSON(s))
}

// GetSessionSteps는 세션의 단계별 진행 상태 목록을 반환한다(프론트 StepProgress[]).
// GET /api/v1/sessions/:id/steps
func (h *Handler) GetSessionSteps(c *gin.Context) {
	h.sessions.mu.Lock()
	defer h.sessions.mu.Unlock()

	s, ok := h.sessions.m[c.Param("id")]
	if !ok {
		h.err(c, http.StatusNotFound, "session not found")
		return
	}

	items := make([]gin.H, 0, len(s.Steps))
	for _, st := range s.Steps {
		items = append(items, gin.H{"step_id": st.StepID, "status": st.Status, "attempts": st.Attempts})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// ValidateStep은 한 단계의 검증을 수행한다.
// STUB — 검증엔진(Kafka) 미연동. 데모용으로 항상 통과 처리하고 다음 단계를 활성화한다.
// POST /api/v1/sessions/:id/validate  body: { "step_id": 1 }
func (h *Handler) ValidateStep(c *gin.Context) {
	var req struct {
		StepID int `json:"step_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.err(c, http.StatusBadRequest, "step_id is required")
		return
	}

	h.sessions.mu.Lock()
	defer h.sessions.mu.Unlock()

	s, ok := h.sessions.m[c.Param("id")]
	if !ok {
		h.err(c, http.StatusNotFound, "session not found")
		return
	}

	idx := -1
	for i := range s.Steps {
		if s.Steps[i].StepID == req.StepID {
			idx = i
			break
		}
	}
	if idx == -1 {
		h.err(c, http.StatusNotFound, "step not found")
		return
	}

	s.Steps[idx].Attempts++
	s.Steps[idx].Status = "passed"

	if idx+1 < len(s.Steps) {
		if s.Steps[idx+1].Status == "pending" {
			s.Steps[idx+1].Status = "active"
		}
		s.CurrentStep = s.Steps[idx+1].StepID
		s.Status = "active"
	} else {
		s.Status = "completed"
	}

	c.JSON(http.StatusOK, gin.H{"status": "passed", "message": "검증을 통과했습니다 (mock)"})
}
