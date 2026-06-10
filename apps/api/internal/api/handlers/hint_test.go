package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/ai"
	"github.com/requset700k/cledyu/api/internal/content"
	"go.uber.org/zap"
)

// hintTestHandler는 실제 임베드 Lab 콘텐츠 + 시드된 stepStore 로 힌트 핸들러를 구성한다.
// aiURL 이 비면 BFF 미설정(정적 폴백) 경로를 검증한다.
func hintTestHandler(t *testing.T, aiURL string) *Handler {
	t.Helper()
	labs, err := content.Load()
	if err != nil {
		t.Fatalf("load lab content: %v", err)
	}
	h := &Handler{log: zap.NewNop(), labs: labs, steps: newStepStore()}
	if aiURL != "" {
		h.ai = ai.New(aiURL, 2*time.Second)
	}
	h.steps.m["s1"] = &sessionSteps{
		LabID:       "lab-linux-basics",
		Steps:       []stepState{{StepID: 1, Status: "active"}, {StepID: 2, Status: "pending"}},
		CurrentStep: 1,
	}
	return h
}

func hintRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/sessions/:id/hint", func(c *gin.Context) {
		c.Set("user_id", "test-user")
		h.RequestHint(c)
	})
	return r
}

func postHint(r *gin.Engine, sessionID string, body map[string]any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sessionID+"/hint", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

// BFF 미설정이면 정적 hint_levels 가 레벨 1→2→3 으로 자동 상승하며 반환된다.
func TestRequestHint_StaticAutoLevel(t *testing.T) {
	h := hintTestHandler(t, "")
	r := hintRouter(h)

	first := decode(t, postHint(r, "s1", map[string]any{"step_id": 1}))
	if first["source"] != "static" || first["hint_level"].(float64) != 1 {
		t.Fatalf("expected static level 1, got %v", first)
	}
	second := decode(t, postHint(r, "s1", map[string]any{"step_id": 1}))
	if second["hint_level"].(float64) != 2 {
		t.Fatalf("expected auto level 2, got %v", second)
	}
	if first["hint"] == second["hint"] {
		t.Error("expected different hints for level 1 vs 2")
	}
	// 레벨 3 도달 후에는 3 에 머문다.
	postHint(r, "s1", map[string]any{"step_id": 1})
	fourth := decode(t, postHint(r, "s1", map[string]any{"step_id": 1}))
	if fourth["hint_level"].(float64) != 3 {
		t.Fatalf("expected level capped at 3, got %v", fourth)
	}
}

func TestRequestHint_ExplicitLevel(t *testing.T) {
	h := hintTestHandler(t, "")
	r := hintRouter(h)
	body := decode(t, postHint(r, "s1", map[string]any{"step_id": 1, "level": 3}))
	if body["hint_level"].(float64) != 3 {
		t.Fatalf("expected level 3, got %v", body)
	}
}

func TestRequestHint_SessionNotFound(t *testing.T) {
	h := hintTestHandler(t, "")
	r := hintRouter(h)
	if w := postHint(r, "unknown", map[string]any{"step_id": 1}); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// BFF 정상 응답 시 AI 힌트(source=ai, model)가 그대로 전달된다.
func TestRequestHint_AIOK(t *testing.T) {
	bff := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var hr ai.HintRequest
		_ = json.NewDecoder(req.Body).Decode(&hr)
		if hr.UserID != "test-user" || hr.LabID != "lab-linux-basics" || hr.Step.ID != 1 {
			http.Error(w, "bad request payload", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(ai.HintResponse{
			Hint: "어떤 명령이 디렉터리를 만들까요?", HintLevel: hr.HintLevel, Model: "gemini-3-pro",
		})
	}))
	defer bff.Close()

	h := hintTestHandler(t, bff.URL)
	r := hintRouter(h)
	body := decode(t, postHint(r, "s1", map[string]any{"step_id": 1}))
	if body["source"] != "ai" || body["model"] != "gemini-3-pro" {
		t.Fatalf("expected ai hint, got %v", body)
	}
}

// BFF 503(ai_unavailable) 이면 정적 hint_levels 로 폴백한다.
func TestRequestHint_AIUnavailable_FallsBackToStatic(t *testing.T) {
	bff := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bff.Close()

	h := hintTestHandler(t, bff.URL)
	r := hintRouter(h)
	w := postHint(r, "s1", map[string]any{"step_id": 1})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 fallback, got %d", w.Code)
	}
	if body := decode(t, w); body["source"] != "static" {
		t.Fatalf("expected static fallback, got %v", body)
	}
}

// BFF 429(Guardrails 한도)는 폴백 없이 사용자에게 그대로 전달된다(한도 우회 방지).
func TestRequestHint_AIRateLimited(t *testing.T) {
	bff := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer bff.Close()

	h := hintTestHandler(t, bff.URL)
	r := hintRouter(h)
	w := postHint(r, "s1", map[string]any{"step_id": 1})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
	if body := decode(t, w); body["code"] != "hint_rate_limited" {
		t.Fatalf("expected hint_rate_limited code, got %v", body)
	}
}
