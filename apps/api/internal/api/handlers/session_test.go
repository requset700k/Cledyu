package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/api/handlers"
	"go.uber.org/zap"
)

// newSessionRouter는 세션/랩 라우트만 가진 테스트 라우터를 만든다.
// JWT stub 대신 user_id를 직접 주입한다.
func newSessionRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handlers.New(newTestConfig(), zap.NewNop())
	r.Use(func(c *gin.Context) {
		c.Set("user_id", "test-user")
		c.Next()
	})
	r.GET("/labs/:id", h.GetLab)
	r.POST("/sessions", h.CreateSession)
	r.GET("/sessions/:id", h.GetSession)
	r.GET("/sessions/:id/steps", h.GetSessionSteps)
	r.POST("/sessions/:id/validate", h.ValidateStep)
	return r
}

func doJSON(t *testing.T, r *gin.Engine, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	var out map[string]any
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w, out
}

func TestGetLab_IncludesSteps(t *testing.T) {
	r := newSessionRouter()
	w, body := doJSON(t, r, http.MethodGet, "/labs/lab-k8s-basics", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	steps, ok := body["steps"].([]any)
	if !ok {
		t.Fatalf("expected steps array in response, got %T", body["steps"])
	}
	if len(steps) != 3 {
		t.Errorf("expected 3 steps for lab-k8s-basics, got %d", len(steps))
	}
}

func TestCreateSession_AndSteps(t *testing.T) {
	r := newSessionRouter()

	w, body := doJSON(t, r, http.MethodPost, "/sessions", map[string]string{"lab_id": "lab-k8s-basics"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	sid, _ := body["id"].(string)
	if sid == "" {
		t.Fatal("expected session id")
	}
	if body["status"] != "ready" {
		t.Errorf("expected status=ready, got %v", body["status"])
	}

	w, steps := doJSON(t, r, http.MethodGet, "/sessions/"+sid+"/steps", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if int(steps["total"].(float64)) != 3 {
		t.Errorf("expected 3 steps, got %v", steps["total"])
	}
	items := steps["items"].([]any)
	first := items[0].(map[string]any)
	if first["status"] != "active" {
		t.Errorf("expected first step active, got %v", first["status"])
	}
}

func TestValidateStep_AdvancesAndCompletes(t *testing.T) {
	r := newSessionRouter()
	_, body := doJSON(t, r, http.MethodPost, "/sessions", map[string]string{"lab_id": "lab-k8s-basics"})
	sid := body["id"].(string)

	// 1단계 검증 → 통과, 2단계 active로 진행.
	w, res := doJSON(t, r, http.MethodPost, "/sessions/"+sid+"/validate", map[string]int{"step_id": 1})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if res["status"] != "passed" {
		t.Errorf("expected passed, got %v", res["status"])
	}

	_, steps := doJSON(t, r, http.MethodGet, "/sessions/"+sid+"/steps", nil)
	items := steps["items"].([]any)
	if items[0].(map[string]any)["status"] != "passed" {
		t.Error("expected step 1 passed")
	}
	if items[1].(map[string]any)["status"] != "active" {
		t.Error("expected step 2 active")
	}

	// 2, 3단계까지 검증 → 세션 completed.
	doJSON(t, r, http.MethodPost, "/sessions/"+sid+"/validate", map[string]int{"step_id": 2})
	doJSON(t, r, http.MethodPost, "/sessions/"+sid+"/validate", map[string]int{"step_id": 3})

	w, sess := doJSON(t, r, http.MethodGet, "/sessions/"+sid, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if sess["status"] != "completed" {
		t.Errorf("expected session completed, got %v", sess["status"])
	}
}

func TestCreateSession_BadLab(t *testing.T) {
	r := newSessionRouter()
	w, _ := doJSON(t, r, http.MethodPost, "/sessions", map[string]string{"lab_id": "nope"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown lab, got %d", w.Code)
	}
}
