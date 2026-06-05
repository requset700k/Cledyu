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

// newSessionRouter는 세션/랩 라우트를 가진 테스트 라우터를 만든다.
// sessions에 nil을 전달해 클러스터 없이 빌드/단위 테스트가 통과하도록 한다 —
// 실제 세션 생성/스텝/검증 흐름은 KubeVirt 클러스터가 필요하므로 통합 테스트로 분리한다.
func newSessionRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handlers.New(newTestConfig(), zap.NewNop(), nil, nil)
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

// TestGetLab_IncludesSteps는 GetLab 응답에 DSL 콘텐츠의 steps가 머지되는지 검증한다.
// sessions 매니저는 필요 없다(콘텐츠 로드만 사용).
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

// TestCreateSession_NoKubeVirt는 sessions 매니저가 nil일 때(클러스터 미연결) 503을 반환하는지 검증한다.
// 실제 VM 생성/세션 흐름은 KubeVirt가 필요하므로 통합 테스트에서 다룬다.
func TestCreateSession_NoKubeVirt(t *testing.T) {
	r := newSessionRouter()
	w, body := doJSON(t, r, http.MethodPost, "/sessions", map[string]string{"lab_id": "lab-k8s-basics"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when sessions=nil, got %d", w.Code)
	}
	if body["error"] == nil {
		t.Error("expected error payload")
	}
}

func TestValidateStep_RequiresStepID(t *testing.T) {
	r := newSessionRouter()

	for _, body := range []map[string]int{{}, {"step_id": 0}} {
		w, res := doJSON(t, r, http.MethodPost, "/sessions/test-session/validate", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %#v, got %d", body, w.Code)
		}
		if res["error"] == nil {
			t.Fatal("expected error payload")
		}
	}
}
