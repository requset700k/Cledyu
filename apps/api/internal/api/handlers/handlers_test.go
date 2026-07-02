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

func TestListLabs_UsesEmbeddedLabDSLMetadata(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/labs", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body struct {
		Items []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
			DurationMin int    `json:"duration_min"`
			StepCount   int    `json:"step_count"`
			VMType      string `json:"vm_type"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != len(body.Items) {
		t.Fatalf("total=%d does not match item count=%d", body.Total, len(body.Items))
	}

	byID := make(map[string]struct {
		Description string
		DurationMin int
		StepCount   int
		VMType      string
	}, len(body.Items))
	for _, item := range body.Items {
		byID[item.ID] = struct {
			Description string
			DurationMin int
			StepCount   int
			VMType      string
		}{
			Description: item.Description,
			DurationMin: item.DurationMin,
			StepCount:   item.StepCount,
			VMType:      item.VMType,
		}
	}

	// 목록 카드의 단계 수와 시간은 Lab YAML DSL을 원천으로 삼아야 한다.
	// 하드코딩 요약값이 남아 있으면 Kubernetes/Docker 카드가 실제 상세 단계와 어긋난다.
	if got := byID["lab-k8s-basics"].StepCount; got != 6 {
		t.Fatalf("lab-k8s-basics step_count=%d, want 6 from DSL", got)
	}
	if got := byID["lab-docker-basics"].StepCount; got != 5 {
		t.Fatalf("lab-docker-basics step_count=%d, want 5 from DSL", got)
	}
	if got := byID["lab-k8s-basics"].DurationMin; got != 75 {
		t.Fatalf("lab-k8s-basics duration_min=%d, want 75 from DSL", got)
	}
	if got := byID["lab-k8s-basics"].Description; got != "단일 노드 k3s 클러스터에서 Pod·Deployment·Service·롤링 업데이트를 직접 실습합니다" {
		t.Fatalf("lab-k8s-basics description=%q does not match DSL", got)
	}
	// 목록 API가 공개하는 vm_type은 실제 세션 프로비저닝에 쓰이는 instancetype과 일치해야 한다.
	// Helm 랩은 chart 패키징/업그레이드 실습 때문에 session.go에서도 lab-medium으로 생성한다.
	if got := byID["lab-helm-advanced"].VMType; got != "lab-medium" {
		t.Fatalf("lab-helm-advanced vm_type=%q, want lab-medium to match provisioning", got)
	}
}

func TestListLabs_KeepsCatalogDisplayOrder(t *testing.T) {
	r := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/labs", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body struct {
		Items []struct {
			ID         string `json:"id"`
			Difficulty string `json:"difficulty"`
		} `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	// 카탈로그는 파일명이나 map iteration 순서가 아니라 실제 학습 난이도와 선행 지식 기준으로 노출한다.
	// Linux/Docker를 입문으로 먼저 배치하고, Kubernetes·Ansible·Terraform은 중급, Helm은 고급으로 둔다.
	want := []string{
		"lab-linux-basics",
		"lab-docker-basics",
		"lab-k8s-basics",
		"lab-ansible-basics",
		"lab-terraform-basics",
		"lab-helm-advanced",
	}
	if len(body.Items) < len(want) {
		t.Fatalf("got %d labs, want at least %d", len(body.Items), len(want))
	}
	for i, wantID := range want {
		if got := body.Items[i].ID; got != wantID {
			t.Fatalf("lab order[%d]=%q, want %q", i, got, wantID)
		}
	}
	// 제목의 "기초" 여부와 난이도는 다를 수 있다. Kubernetes는 기초 Lab이지만 Docker 이후에 다루는
	// 클러스터 리소스 실습이므로 beginner가 아니라 intermediate로 유지한다.
	wantDifficulties := []string{"beginner", "beginner", "intermediate", "intermediate", "intermediate", "advanced"}
	for i, wantDifficulty := range wantDifficulties {
		if got := body.Items[i].Difficulty; got != wantDifficulty {
			t.Fatalf("lab difficulty[%d]=%q, want %q", i, got, wantDifficulty)
		}
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
			Commands    any      `json:"commands"`
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
	// 공개 필드(제목/설명)는 유지되어야 한다. 정답 역할을 하는 명령과 검증 조건은
	// 브라우저 Network 응답으로도 새지 않아야 한다.
	if body.Steps[0].Title == "" || body.Steps[0].Description == "" {
		t.Fatalf("expected public step fields to remain, got %+v", body.Steps[0])
	}
	if body.Steps[0].Commands != nil {
		t.Fatalf("commands must not be exposed in lab detail, got %+v", body.Steps[0].Commands)
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
