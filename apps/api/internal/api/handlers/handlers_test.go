package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/requset700k/cledyu/api/internal/api/handlers"
	"github.com/requset700k/cledyu/api/internal/config"
	"go.uber.org/zap"
)

func newTestConfig() *config.Config {
	return &config.Config{
		Server:      config.ServerConfig{Mode: "debug"},
		FrontendURL: "https://app.test",
		Keycloak: config.KeycloakConfig{
			URL:          "https://keycloak.test",
			Realm:        "test",
			ClientID:     "test-client",
			RedirectURI:  "https://api.test/api/v1/auth/callback",
			CookieDomain: ".test",
		},
	}
}

func newTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// validator=nil(mock 검증), authProvider=nil(OIDC discovery 없이 핸들러 단위만 — 인증 흐름은 503).
	h := handlers.New(newTestConfig(), zap.NewNop(), nil, nil, nil, nil, nil, nil, nil)

	r.GET("/health", h.Health)
	r.GET("/ready", h.Ready)
	r.GET("/me", func(c *gin.Context) {
		c.Set("user_id", "test-user")
		c.Set("user_email", "test@cledyu.local")
		c.Set("user_name", "Test User")
		c.Set("user_role", "student")
		h.GetMe(c)
	})
	r.GET("/labs", h.ListLabs)
	r.GET("/labs/:id", h.GetLab)
	r.GET("/api/v1/auth/login", h.Login)
	return r
}

func TestHealth(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", body["status"])
	}
}

func TestReady_DebugAllowsMissingExternalDependencies(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 in debug mode, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", body["status"])
	}
}

func TestReady_ReleaseReportsMissingExternalDependencies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := newTestConfig()
	cfg.Server.Mode = "release"
	h := handlers.New(cfg, zap.NewNop(), nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.GET("/ready", h.Ready)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 in release mode with lab content loaded, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", body["status"])
	}
	checks, ok := body["checks"].(map[string]any)
	if !ok {
		t.Fatalf("expected checks object, got %T", body["checks"])
	}
	keycloak, ok := checks["keycloak"].(map[string]any)
	if !ok {
		t.Fatalf("expected keycloak check, got %T", checks["keycloak"])
	}
	if keycloak["status"] != "degraded" {
		t.Errorf("expected keycloak degraded detail in release mode, got %v", keycloak)
	}
}

func TestListLabs(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/labs", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["total"].(float64) == 0 {
		t.Error("expected at least one lab")
	}
}

func TestGetLab_Found(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/labs/lab-k8s-basics", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestGetLab_HidesValidationChecks(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/labs/lab-linux-basics", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body struct {
		Environment string `json:"environment"`
		Steps       []struct {
			ID          int      `json:"id"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Commands    []string `json:"commands"`
			HintLevels  []string `json:"hint_levels"`
			Checks      any      `json:"checks"`
		} `json:"steps"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Environment != "ubuntu" {
		t.Fatalf("expected environment=ubuntu, got %q", body.Environment)
	}
	if len(body.Steps) == 0 {
		t.Fatal("expected steps in lab detail response")
	}
	// 공개 필드(제목/명령)는 유지되어야 한다. 힌트는 /hint 엔드포인트가 레벨 1→2→3 으로
	// 단계 제공하므로 랩 상세에는 노출하지 않는다(legacy hint 도 콘텐츠에서 제거됨).
	if body.Steps[0].Title == "" || len(body.Steps[0].Commands) == 0 {
		t.Fatalf("expected public step fields to remain, got %+v", body.Steps[0])
	}
	if body.Steps[0].Checks != nil {
		t.Fatalf("validation checks must not be exposed, got %+v", body.Steps[0].Checks)
	}
	// 정적 힌트 3종 전체가 상세 응답으로 새면 단계적 힌트 설계가 무력화된다.
	if len(body.Steps[0].HintLevels) != 0 {
		t.Fatalf("hint_levels must not be exposed in lab detail, got %+v", body.Steps[0].HintLevels)
	}
}

func TestGetLab_NotFound(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/labs/nonexistent", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGetMe(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["id"] != "test-user" {
		t.Errorf("expected id=test-user, got %v", body["id"])
	}
}

// authProvider 미설정(Keycloak 미가용) 시 로그인은 503 으로 안전하게 막힌다.
// 실 OIDC 리다이렉트 흐름은 Keycloak 가용 환경의 통합 테스트에서 검증한다.
func TestLogin_Unavailable_WhenNoProvider(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without auth provider, got %d", w.Code)
	}
}
