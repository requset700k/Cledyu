package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/content"
	"go.uber.org/zap"
)

// ownershipHandler는 stepStore 에 alice 소유 세션(s1)과 무소유 레거시 세션(s0)을 시드한다.
func ownershipHandler(t *testing.T) *Handler {
	t.Helper()
	labs, err := content.Load()
	if err != nil {
		t.Fatalf("load lab content: %v", err)
	}
	h := &Handler{log: zap.NewNop(), labs: labs, steps: newStepStore()}
	h.steps.m["s1"] = &sessionSteps{
		LabID:  "lab-linux-basics",
		UserID: "alice",
		Steps:  []stepState{{StepID: 1, Status: "active"}},
	}
	h.steps.m["s0"] = &sessionSteps{
		LabID: "lab-linux-basics",
		Steps: []stepState{{StepID: 1, Status: "active"}},
	}
	return h
}

// ownershipRouter는 uid 신원으로 stepStore 기반 보호 라우트 3종을 등록한다.
func ownershipRouter(h *Handler, uid string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	identify := func(c *gin.Context) {
		if uid != "" {
			c.Set("user_id", uid)
		}
		c.Next()
	}
	r.GET("/sessions/:id/steps", identify, h.GetSessionSteps)
	r.POST("/sessions/:id/validate", identify, h.ValidateStep)
	r.POST("/sessions/:id/hint", identify, h.RequestHint)
	return r
}

func doJSON(r *gin.Engine, method, path string, body map[string]any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// 타인(bob)은 alice 세션의 steps/validate/hint 에 모두 404 를 받는다(존재 비노출).
func TestOwnership_DeniesOtherUser(t *testing.T) {
	h := ownershipHandler(t)
	r := ownershipRouter(h, "bob")

	cases := []struct {
		name string
		do   func() *httptest.ResponseRecorder
	}{
		{"steps", func() *httptest.ResponseRecorder { return doJSON(r, http.MethodGet, "/sessions/s1/steps", nil) }},
		{"validate", func() *httptest.ResponseRecorder {
			return doJSON(r, http.MethodPost, "/sessions/s1/validate", map[string]any{"step_id": 1})
		}},
		{"hint", func() *httptest.ResponseRecorder {
			return doJSON(r, http.MethodPost, "/sessions/s1/hint", map[string]any{"step_id": 1})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := tc.do(); w.Code != http.StatusNotFound {
				t.Fatalf("expected 404 for non-owner, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

// 소유자(alice)는 정상 접근된다.
func TestOwnership_AllowsOwner(t *testing.T) {
	h := ownershipHandler(t)
	r := ownershipRouter(h, "alice")

	if w := doJSON(r, http.MethodGet, "/sessions/s1/steps", nil); w.Code != http.StatusOK {
		t.Fatalf("steps: expected 200 for owner, got %d", w.Code)
	}
	// validator nil → mock 통과 경로까지 도달해야 한다(소유자 가드에 막히지 않음).
	if w := doJSON(r, http.MethodPost, "/sessions/s1/validate", map[string]any{"step_id": 1}); w.Code != http.StatusOK {
		t.Fatalf("validate: expected 200 for owner, got %d", w.Code)
	}
	// ai nil → 정적 hint_levels 폴백으로 200.
	if w := doJSON(r, http.MethodPost, "/sessions/s1/hint", map[string]any{"step_id": 1}); w.Code != http.StatusOK {
		t.Fatalf("hint: expected 200 for owner, got %d", w.Code)
	}
}

// 레거시 무소유 세션(UserID 빈 값)은 누구든 접근 가능(하위호환), 신원 없는 요청도 통과.
func TestOwnership_LegacyAndAnonymousAllowed(t *testing.T) {
	h := ownershipHandler(t)

	if w := doJSON(ownershipRouter(h, "bob"), http.MethodGet, "/sessions/s0/steps", nil); w.Code != http.StatusOK {
		t.Fatalf("legacy session: expected 200, got %d", w.Code)
	}
	if w := doJSON(ownershipRouter(h, ""), http.MethodGet, "/sessions/s1/steps", nil); w.Code != http.StatusOK {
		t.Fatalf("anonymous request: expected 200, got %d", w.Code)
	}
}

// 미존재 세션은 가드를 통과해 기존 404 경로로 떨어진다.
func TestOwnership_UnknownSessionStill404(t *testing.T) {
	h := ownershipHandler(t)
	r := ownershipRouter(h, "alice")
	if w := doJSON(r, http.MethodGet, "/sessions/nope/steps", nil); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
